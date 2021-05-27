# 消息编码

## 消息结构

典型的RPC调用如下

```go
    err = client.Call("服务名.方法名",参数,响应)
```

将请求的参数和响应的返回值抽象为body，剩余的信息存放在header中，构建消息结构。

```go
    type Header struct {
        ServiceMethod string // "格式为：Service.Method"
        Seq           uint64 // 客户端请求的序列号
        Error         string // 存储服务端的错误信息
    }
```

一个请求的消息结构如下：

```go
    type request struct {
        h            *codec.Header  // 消息头
        argv, replyv reflect.Value // 请求参数和响应返回值，类型需要通过反射在运行时确定
    }
```

## 消息编解码

构建消息编解码接口Codec，可以用不同的编码方式实现Codec接口创建不同的Codec实例，而对外提供的是统一的Codec接口。

```go
    // 编解码消息体的接口(可以实现不同的Codec实例，即不同编码方式)
    type Codec interface {
        io.Closer
        ReadHeader(*Header) error
        ReadBody(interface{}) error
        Write(*Header, interface{}) error
    }
```

构造一个Codec接口是通过传递一个可读写的io接口作为数据读写的媒介实现的。

```go
    type NewCodecFunc func(io.ReadWriteCloser) Codec
```

通过实现Codec接口的方法可以构造Codec类型作为消息编解码的方式，本文采用的是gob。
gob是Go语言自己以二进制形式序列化和反序列化程序数据的格式，常用于RPC参数和结果的传输，仅仅适用于Go语言写的服务之间的通信，利用了Go的反射进行编码和解码。

```go
    type GobCodec struct { // 实现Codec接口的所有方法
        conn io.ReadWriteCloser // socket连接实例
        buf  *bufio.Writer      // 防止阻塞的带缓冲Writer
        dec  *gob.Decoder
        enc  *gob.Encoder
    }
```

gob编码方式实现的Codec接口需要有gob的编码器和解码器，也需要传输数据的连接conn。conn对应socket连接，服务端在收到客户端的请求后建立的连接传递给GobCodec的conn，包含了请求的所有数据。
**GobCodec相当于封装了原始的socket连接，在此基础上添加了消息编解码方式**。

```go
    func NewGobCodec(conn io.ReadWriteCloser) Codec {
        buf := bufio.NewWriter(conn)
        return &GobCodec{
            conn: conn,
            buf:  buf,
            dec:  gob.NewDecoder(conn), // 从conn中读取数据
            enc:  gob.NewEncoder(buf),
        }
    }
```

由于解码器需要从连接中读取消息，因此dec通过conn进行初始化。编码器则通过bufio.Writer结构来初始化，但因为编码器编码信息后也是通过相同的连接传递响应，因此bufio.Writer结构选择conn作为底层的I/O写接口。

## 通信过程

对于GeeRPC框架来说，目前客户端和服务端需要协商的信息是消息的编码方式，因此将这个信息存放到Option结构中，固定采用JSON编码，每个传输报文的格式为：Option(记录编码方式)|Header(头部信息)|body(请求参数和响应)，一个连接（即一个报文）可以包含多个header和body。

```go
    type Option struct { // 存储消息编码方式，固定采用JSON编码Option置于报文头部
        MagicNumber int     // GeeRPC特有 约定一个数字用于代表GeeRPC框架
        CodecType   codec.Type // 编码类型
    }
```

### 服务端

服务端需要监听端口，当有连接请求时建立连接，用该连接创建一个Codec实例**解码客户端发送过来的请求，编码返回给客户端的响应**

```go
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
```

net.Listener是go标准库net中用于监听端口网络通信的接口，实现了三个方法：

1. Accept() (Conn, error)   监听端口，发现有连接请求时返回连接Conn，Conn是实现了读写方法的网络I/O接口
2. Close() error
3. Addr() Addr  返回监听的网络地址

服务端处理连接时，首先通过JSON解码得到报文头部的Option信息，确定编码方式，再用对应的解码器解码报文正文。

```go
    func (server *Server) ServeConn(conn io.ReadWriteCloser) {
        // 解析获取报文编码方式 Option结构
        var opt Option
        if err := json.NewDecoder(conn).Decode(&opt); err != nil {
            log.Println("rpc server: options error: ", err)
            return
        }
        if opt.MagicNumber != MagicNumber {
            log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
            return
        }
        f := codec.NewCodecFuncMap[opt.CodecType]   // 根据编码方式创建Codec实例
        if f == nil {
            log.Printf("rpc server: invalid codec type %s", opt.CodecType)
            return
        }
        server.serveCodec(f(conn)) // 处理报文信息
    }
```

解码报文正文分三步：

1. 解码请求的消息内容。
2. 处理请求。异步处理请求，但由于一个连接可以有多个请求消息，因此需要借助sync.WaitGroup等待处理完所有请求之后再关闭连接。
3. 回复请求。回复请求的报文必须逐个发送，因为并发容易导致多个回复报文交织在一起，所以采用互斥锁sync.Mutex进行限制。

```go
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
```

### 客户端

客户端通过```net.Dail(传输协议,端口地址)```建立与服务端的连接，再用得到的连接创建Codec实例用于**编码发送给服务端的消息，解码从服务端收到的消息**。

```go
    // 首先发送JSON编码的option协商消息的编码信息
    _ = json.NewEncoder(conn).Encode(geerpc.DefaultOption)
    cc := codec.NewGobCodec(conn) // 采用gob编码
    for i := 0; i < 5; i++ {
        h := &codec.Header{
        ServiceMethod: "Foo.Sum",
        Seq:           uint64(i),
        }
        _ = cc.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
        // 报文的格式是header|body 而gob的Encode方法是依次读取数据流的下一个值 因此要县区header的内容再取body的内容
        _ = cc.ReadHeader(h)
        var reply string
        _ = cc.ReadBody(&reply)
        log.Println("reply:", reply)
    }
```
