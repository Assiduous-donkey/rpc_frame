package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct { // 实现Codec接口的所有方法
	conn io.ReadWriteCloser // socket链接实例
	buf  *bufio.Writer      // 防止阻塞的带缓冲Writer
	dec  *gob.Decoder
	enc  *gob.Encoder
}

var _ Codec = (*GobCodec)(nil)

func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn), // 从conn中读取数据
		enc:  gob.NewEncoder(buf),
	}
}

// 读操作——解码decode
// Decode方法：从socket连接conn中读取内容存储在参数中
func (c *GobCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}

func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

// 写操作——编码encode
// Encode方法：将数据进行gob编码然后写入到socket连接conn中
func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		_ = c.buf.Flush() // 把缓冲区中的数据写入底层的io.Writer，即conn
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = c.enc.Encode(h); err != nil {
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}
	if err = c.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob error encoding body:", err)
		return err
	}
	return nil
}

// 实际上是关闭socket连接
func (c *GobCodec) Close() error {
	return c.conn.Close()
}
