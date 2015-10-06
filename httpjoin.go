package httpjoin // name..? broadcaster ..? uniquerequest ..? uniquehttp ?

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
)

// TODO: unexport the writers here...?

var (
	counter int64 = 1
)

func Broadcast(methods ...string) func(next http.Handler) http.Handler {
	var requestsMu sync.Mutex
	requests := make(map[string]*BroadcastWriter)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Println("broadcast handler..")

			var reqKey = fmt.Sprintf("%s %s", r.Method, r.URL.RequestURI())
			var bw *BroadcastWriter
			var lw *listener

			requestsMu.Lock()
			bw, ok := requests[reqKey]
			if ok {
				id := atomic.LoadInt64(&counter)
				atomic.AddInt64(&counter, 1)

				lw = newListener(w, id)
				x := bw.AddListener(lw)
				if !x {
					panic("couldnt add listener..")
				}
				// TODO: if false.. then, just go to next.ServeHTTP(w, r)
				// ...
			}
			requestsMu.Unlock()

			if ok {
				// existing request listening for the stuff..
				log.Println("waiting for existing request..")

				// TODO: hmm.. do we have a listener timeout..?
				// after that point, we close it up.. etc..?
				// ie. what if the first handler never responds..?
				// .. we should probably use context.Context ..
				// or, have a options with a timeout..

				select {
				case <-lw.Done():
					return
				}
				return
			}

			bw = NewBroadcastWriter(w)

			requestsMu.Lock()
			log.Println("!!!!! SET requests[reqKey] !!!!!")
			requests[reqKey] = bw
			requestsMu.Unlock()

			log.Println("sending request to next.ServeHTTP(bw,r)")
			next.ServeHTTP(bw, r)

			// Remove the request key from the map in case a request comes
			// in while we're writing to the listeners
			requestsMu.Lock()
			delete(requests, reqKey)
			requestsMu.Unlock()

			bw.Flush()
			// bw.Flush() // test.. call .Flush() again here..
		})
	}
}

type listener struct {
	ID int64
	http.ResponseWriter
	wroteHeaderCh chan struct{}
	flushedCh     chan struct{}
}

func newListener(w http.ResponseWriter, id int64) *listener {
	log.Println("newListener, id:", id)
	return &listener{
		ID:             id,
		ResponseWriter: w,
		wroteHeaderCh:  make(chan struct{}, 1),
		flushedCh:      make(chan struct{}, 1),
	}
}

func (lw *listener) Done() <-chan struct{} {
	return lw.flushedCh
}

// TODO: gotta implement Write() and WriteHeader() ...
// just to block on headerSentCh()
// .. and to close it..?

type BroadcastWriter struct { // Rename Broadcaster ...?
	listeners []*listener
	header    http.Header
	bufw      *bytes.Buffer

	wroteHeader uint32
	flushed     uint32

	mu sync.Mutex
}

func NewBroadcastWriter(w http.ResponseWriter) *BroadcastWriter {
	id := atomic.LoadInt64(&counter)
	atomic.AddInt64(&counter, 1)

	return &BroadcastWriter{
		listeners: []*listener{newListener(w, id)},
		header:    http.Header{},
		bufw:      &bytes.Buffer{},
	}
}

// TODO: we should add a bool to confirm
// the listener was added.. and a lock on headerSentMu perhaps,
// cuz, once the header is sent, we can't accept any more listeners..
func (w *BroadcastWriter) AddListener(lw *listener) bool {
	if atomic.LoadUint32(&w.wroteHeader) > 0 {
		return false
	}

	// note: we need to synchronize the listeners..
	w.mu.Lock()
	defer w.mu.Unlock()
	w.listeners = append(w.listeners, lw)
	return true
}

// TODO: RemoveListener ...?
// or Subscribe() and Unsubscribe() ?

func (w *BroadcastWriter) Header() http.Header {
	return w.header
}

func (w *BroadcastWriter) Write(p []byte) (int, error) {
	// hmm.. should we have a separate buffer for each listener..?
	// TODO: sycnrhonized?
	// w.mu.Lock()
	// defer w.mu.Unlock()

	if atomic.LoadUint32(&w.wroteHeader) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.bufw.Write(p)
}

func (w *BroadcastWriter) WriteHeader(status int) {
	log.Println("Broadcast WriterHeader():.", atomic.LoadUint32(&w.wroteHeader))
	if atomic.LoadUint32(&w.wroteHeader) > 0 {
		return
	}
	atomic.AddUint32(&w.wroteHeader, 1)

	log.Println("listeners...?", len(w.listeners))

	// w.mu.Lock()
	// defer w.mu.Unlock()

	for _, lw := range w.listeners {
		log.Println("=====> writeHeader()", lw.ID)
		go func(lw *listener, status int, header http.Header) {
			h := map[string][]string(lw.Header())
			for k, v := range header {
				h[k] = v
			}
			h["X-ID"] = []string{fmt.Sprintf("%d", lw.ID)}

			lw.WriteHeader(status)
			lw.wroteHeaderCh <- struct{}{}
		}(lw, status, w.header)
	}
}

// how does http streaming work...? can we broadcast streaming...?
// best is to make a test case really..
// but first, how does Flush() operate normally?
// what happens to connections normally after a request..? etc.?
func (w *BroadcastWriter) Flush() {
	if atomic.LoadUint32(&w.flushed) > 0 {
		// TODO: should we print an error or something...?
		return
	}
	atomic.AddUint32(&w.flushed, 1)

	if atomic.LoadUint32(&w.wroteHeader) == 0 {
		w.WriteHeader(http.StatusOK)
	}

	log.Println("flushing..")

	// w.mu.Lock()
	// defer w.mu.Unlock()

	data := w.bufw.Bytes()

	for _, lw := range w.listeners {
		go func(lw *listener, data []byte) {
			// Block until the header has been written
			<-lw.wroteHeaderCh
			// close(lw.wroteHeaderCh) // ..?

			log.Println("=====> write()", lw.ID, "-", string(data))

			lw.Write(data)

			lw.flushedCh <- struct{}{}
			close(lw.flushedCh)
		}(lw, data)
	}
}
