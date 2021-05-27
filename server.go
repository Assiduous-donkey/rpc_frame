package geerpc

import (
	"encoding/json"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
)

const MagicNumber = 0x3bef5c

type Option struct { // 存储消息编码方式，固定采用JSON编码Option置于报文头部
	MagicNumber int
	CodecType   codec.Type // 编码类型
}

var DefaultOption = &Option{ // 默认使用Gob编码
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

type Server struct{}

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error: ", err)
			return
		}
		go server.ServeConn(conn)
	}
}

func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}

func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	// 解析获取报文编码方式
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serveCodec(f(conn)) // 处理报文信息
}

var invalidRequest = struct{}{} // 发生错误时响应的占位符

// 分三个阶段：读取请求、处理请求、回复请求
// 一次连接允许接收多个请求
// 回复请求的报文必须逐个发送，因为并发容易导致多个回复报文交织在一起
func (server *Server) serveCodec(cc codec.Codec) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup) // 处理完所有请求后再关闭连接
	for {
		req, err := server.readRequest(cc) // 底层采用gob的Decode方法 读取底层I/O流的下一个报文段
		if err != nil {                    // 请求出错
			if req == nil {
				break
			}
			req.h.Error = err.Error() // 设置错误信息
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	_ = cc.Close()
}

// 一个RPC的请求信息
type request struct {
	h            *codec.Header
	argv, replyv reflect.Value // 类型需要通过反射在运行时确定
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		// io.ErrUnexpectedEOF表示读取固定大小的块或数据结构时遇到EOF
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error: ", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	// 构建完整的请求消息结构
	req := &request{h: h}
	req.argv = reflect.New(reflect.TypeOf("")) // 假设argv是字符串类型
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err: ", err)
	}
	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	// 对同一个连接的多个请求依次回复
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error: ", err)
	}
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// 等待同一个连接的多个请求处理完再关闭连接
	defer wg.Done()
	// log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq))
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}
