package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"sync"
)

// 承载一次RPC调用所需的信息
type Call struct {
	Seq           uint64
	ServiceMethod string // "service.method"
	Args          interface{}
	Reply         interface{}
	Error         error
	Done          chan *Call // 用于支持异步调用
}

// 通知调用方调用结束
func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	cc       codec.Codec // 消息编解码器 序列化发送的请求，反序列化收到的响应
	opt      *Option
	sending  sync.Mutex // 确保请求有序发送，防止多个请求报文混淆
	header   codec.Header
	mu       sync.Mutex       // 并发情况下一个变量可能同时被多个goroutine修改，因此使用互斥锁
	seq      uint64           // 每个请求的唯一编号，递增
	pending  map[uint64]*Call // 存储未处理完的请求
	closing  bool             // 用户主动关闭客户端
	shutdown bool             // 有错误导致客户端关闭
}

var ErrShutdown = errors.New("connection is shut down")

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown // 不可重复关闭同一个连接
	}
	client.closing = true
	return client.cc.Close()
}

var _ io.Closer = (*Client)(nil)

func (client *Client) IsAvailable() bool { // 客户端是否可用
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.closing && !client.shutdown
}

// 在客户端中添加Call
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.seq += 1 // 加互斥锁 避免并发修改client.seq
	client.pending[call.Seq] = call
	return call.Seq, nil
}

// 从客户端中移除Call
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq) // map非并发安全
	return call
}

// 服务端或客户端出错时通知客户端中所有还未执行完的Call
func (client *Client) terminateCall(err error) {
	client.sending.Lock() // Call可能正在发送 因此先加sending互斥锁
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()

	client.shutdown = true // 出错导致客户端需要关闭
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

/* ****************************************************
客户端接收响应 启动客户端后异步执行，专门负责接收服务端响应
**************************************************** */
func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq) // 取出Call信息处理调用情况
		switch {                         // 报文格式为 Header|body 因此无论如何对每个请求都要依次读取Header和body
		case call == nil: // 请求发送不完整或者被取消，但服务端仍然处理了
			err = client.cc.ReadBody(nil)
		case h.Error != "": // 服务端处理出错
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done() // 处理完call
		default: // 正常处理
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done() // 处理完call
		}
	}
	// 出错
	client.terminateCall(err)
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	// 首先需要向服务端发送option协商报文编码方式
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error: ", err)
		return nil, err
	}
	// 必须成功发送option才能成功创建客户端
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1, // 初始值
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive() // 异步接收响应
	return client
}

// 将option设置为可选参数
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of option is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// 创建客户端的入口 一个客户端只处理一个连接(address)的信息
// network：通信方式TCP/UDP
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address) // 建立连接的方式
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil { // 创建客户端失败则关闭net.Dial建立的连接
			_ = conn.Close()
		}
	}()
	return NewClient(conn, opt)
}

func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()

	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// 初始化请求头
	client.header.Seq = seq
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Error = ""

	// 编码——发送 body为请求参数
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// 异步发起的RPC调用
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 1)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call // 异步体现在没有等待调用完成：call.done()
}

// 同步RPC调用
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	// 阻塞等待调用完成
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done // 阻塞直到执行了 call.done()
	return call.Error
}
