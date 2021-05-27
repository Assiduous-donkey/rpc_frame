package codec

import "io"

type Header struct {
	ServiceMethod string // "格式为：Service.Method"
	Seq           uint64 // 客户端请求的序列号
	Error         string // 存储服务端的错误信息
}

// 编解码消息体的接口(可以实现不同的Codec实例，即不同编码方式)
// 本质是一个控制数据传输的字节流
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

type NewCodecFunc func(io.ReadWriteCloser) Codec
type Type string

const (
	GobType Type = "application/gob" // gob编码方式
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
