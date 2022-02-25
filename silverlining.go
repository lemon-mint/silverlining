package silverlining

import (
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/go-www/h1"
	"github.com/go-www/silverlining/gopool"
)

type Handler func(r *Context)

const (
	ServerStoped uint8 = iota
	ServerStarting
	ServerRunning
	ServerStopping
)

type Server struct {
	Listener net.Listener // Listener for incoming connections

	MaxBodySize int64 // Max body size (default: 2MB)

	Handler Handler // Handler to invoke for each request

	serverStatus uint8 // Server status (stoped: 0, starting: 1, running: 2, stopping: 3)

	ReadTimeout time.Duration
}

func (s *Server) Serve(l net.Listener) error {
	s.serverStatus = ServerStarting
	s.Listener = l

	if s.MaxBodySize == 0 {
		s.MaxBodySize = 2 * 1024 * 1024
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			if s.serverStatus == ServerStopping {
				return nil
			}
			return err
		}

		gopool.Go(func() {
			s.ServeConn(conn)
		})
	}
}

var buffer8kPool sync.Pool = sync.Pool{
	New: func() interface{} {
		v := make([]byte, 8*1024)
		return &v
	},
}

func getBuffer8k() *[]byte {
	return buffer8kPool.Get().(*[]byte)
}

func putBuffer8k(b *[]byte) {
	*b = (*b)[:cap(*b)]
	buffer8kPool.Put(b)
}

/*
type logConn struct {
	c net.Conn
}

func (lc *logConn) Read(b []byte) (n int, err error) {
	n, err = lc.c.Read(b)
	log.Printf("Read: %d bytes\n", n)
	log.Println(string(b[:n]))
	return
}

func (lc *logConn) Write(b []byte) (n int, err error) {
	log.Printf("Write: %d bytes\n", len(b))
	log.Println(string(b))
	return lc.c.Write(b)
}

func (lc *logConn) Close() error {
	log.Println("Close")
	return lc.c.Close()
}
*/

func (s *Server) ServeConn(conn net.Conn) {
	var hijack bool

	defer func() {
		if hijack {
			return
		}
		conn.Close()
	}()

	readBuffer := getBuffer8k()
	//defer putBuffer8k(readBuffer)
	defer func() {
		if hijack {
			return
		}
		putBuffer8k(readBuffer)
	}()

	//reqCtx := GetRequestContext(&logConn{conn})
	reqCtx := GetRequestContext(conn)
	//defer PutRequestContext(reqCtx)
	defer func() {
		if hijack {
			return
		}
		PutRequestContext(reqCtx)
	}()
	reqCtx.server = s
	//reqCtx.conn = &logConn{conn}
	reqCtx.conn = conn
	reqCtx.rawconn = conn
	reqCtx.reqR = h1.RequestReader{
		//R: &logConn{conn},
		R:          conn,
		ReadBuffer: *readBuffer,
		NextBuffer: nil,
		Request:    h1.Request{},
	}

	for {
		if s.ReadTimeout > 0 {
			conn.SetReadDeadline(time.Now().Add(s.ReadTimeout))
		}
		_, err := reqCtx.reqR.Next()
		if err != nil {
			if err == io.EOF {
				//log.Println("EOF")
				return
			}
			log.Println(err)
			return
		}
		reqCtx.resetSoft()

		//println("Request:", reqCtx.reqR.Request.Method, string(reqCtx.reqR.Request.URI))

		s.Handler(reqCtx)

		if reqCtx.hijack {
			hijack = true
			return
		}

		if reqCtx.respW.ContentLength == -1 {
			err = reqCtx.respW.Flush()
			if err != nil {
				log.Println(err)
				return
			}
			return
		}

		//println("Response:", reqCtx.response.StatusCode)

		if reqCtx.reqR.Remaining() == 0 {
			err = reqCtx.respW.Flush()
			if err != nil {
				log.Println(err)
				return
			}
		}
	}
}

type Method = h1.Method

const (
	MethodGET     = h1.MethodGET
	MethodHEAD    = h1.MethodHEAD
	MethodPOST    = h1.MethodPOST
	MethodPUT     = h1.MethodPUT
	MethodDELETE  = h1.MethodDELETE
	MethodCONNECT = h1.MethodCONNECT
	MethodOPTIONS = h1.MethodOPTIONS
	MethodTRACE   = h1.MethodTRACE
	MethodPATCH   = h1.MethodPATCH
	MethodBREW    = h1.MethodBREW
)

type Context struct {
	server *Server

	response Response
	hwt      bool
	br       *h1.BodyReader

	respW *h1.Response
	reqR  h1.RequestReader

	conn io.ReadWriteCloser

	rawconn net.Conn

	hijack bool
}

func (r *Context) Write(p []byte) (n int, err error) {
	r.WriteHeader(r.response.StatusCode)
	return r.respW.Write(p)
}

func (r *Context) WriteHeader(status int) {
	if !r.hwt {
		r.response.StatusCode = status
		r.hwt = true
		r.respW.WriteHeader(r.response.StatusCode)
		err := r.writeUserHeader()
		if err != nil {
			log.Println(err)
		}
		_, err = r.respW.Write(crlf)
		if err != nil {
			log.Println(err)
		}
	}
}

var headersep = []byte(": ")
var crlf = []byte("\r\n")

func (r *Context) writeUserHeader() error {
	for i := range r.response.Headers {
		if !r.response.Headers[i].Disabled {
			_, err := r.respW.WriteString(r.response.Headers[i].Name)
			if err != nil {
				return err
			}
			_, err = r.respW.Write(headersep)
			if err != nil {
				return err
			}
			_, err = r.respW.WriteString(r.response.Headers[i].Value)
			if err != nil {
				return err
			}
			_, err = r.respW.Write(crlf)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Context) BodyReader() *h1.BodyReader {
	if r.br == nil {
		r.br = r.reqR.Body()
	}
	return r.br
}
func (r *Context) CloseBodyReader() {
	if r.br != nil {
		h1.PutBodyReader(r.br)
		r.br = nil
	}
}

type ResponseType uint8

const (
	ResponseTypeNone ResponseType = iota
	ResponseTypeFullBody
	ResponseTypeStream
	ResponseTypeHijack
	ResponseTypeUser
)

type Response struct {
	Headers []Header

	// FullBody []byte    // for ResponseTypeFullBody
	// Stream   io.Reader // for ResponseTypeStream
	// Hijack   func() (io.ReadCloser, io.Writer)

	StatusCode int

	// BodyType ResponseType
}

type Header struct {
	Disabled bool

	Name  string
	Value string
}

func (resp *Response) reset() {
	resp.StatusCode = 200
	resp.Headers = resp.Headers[:0]
}

func (resp *Context) RawURI() []byte {
	return resp.reqR.Request.RawURI
}

func (resp *Context) Path() []byte {
	return resp.reqR.Request.URI.Path()
}

func (resp *Context) QueryParams() []h1.Query {
	return resp.reqR.Request.URI.Query()
}

func (resp *Context) GetQueryParam(name []byte) (string, error) {
	return resp.reqR.Request.URI.QueryValue(name)
}

func (r *Context) SetContentLength(length int) {
	r.respW.ContentLength = length
}

var requestPool = sync.Pool{
	New: func() interface{} {
		v := new(Context)
		return v
	},
}

func GetRequestContext(upstream io.Writer) *Context {
	ctx := requestPool.Get().(*Context)
	ctx.respW = h1.GetResponse(upstream)
	return ctx
}

func PutRequestContext(ctx *Context) {
	ctx.resetHard()
	requestPool.Put(ctx)
}

func (r *Context) resetSoft() {
	r.hwt = false
	r.CloseBodyReader()
	r.response.reset()
}

func (r *Context) resetHard() {
	r.resetSoft()
	r.conn = nil
	r.rawconn = nil
	r.server = nil
	r.hijack = false
	r.reqR.Reset()
	h1.PutResponse(r.respW)
}

func (r *Context) HijackConn() (bufR h1.HijackReader, bufW *h1.Response, conn net.Conn) {
	bufR = r.reqR.Hijack()
	bufW = r.respW
	conn = r.rawconn
	r.hijack = true

	return
}
