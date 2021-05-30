package geerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

/* 通过反射实现结构体与服务的映射关系 */

type methodType struct { // 存储一个方法的完整信息
	method    reflect.Method
	ArgType   reflect.Type
	ReplyType reflect.Type
	numCalls  uint64 // 统计方法调用次数
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// 服务参数可以是指针类型也可以是值类型
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem()) // 对于指针类型返回指针
	} else {
		argv = reflect.New(m.ArgType).Elem() // 非指针类型返回值类型 由Elem()获取
	}
	return argv
}

func (m *methodType) newReplyv() reflect.Value {
	// 通过reflect.Elem()方法获取指针指向的元素类型
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map: // 申请内存 slice和map对象使用前都需要先申请内存
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

type service struct {
	name   string                 // 映射的结构体名称
	typ    reflect.Type           // 结构体类型
	rcvr   reflect.Value          // 结构体的实例本身
	method map[string]*methodType // 存储结构体所有可以映射为服务的方法
}

// 入参是任意需要映射为服务的结构体实例
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name() // 若s.rcvr是指针则Indirect返回s.rcvr指向的值
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type // 方法的类型——函数类型，包含了函数参数和返回值等信息
		// 可映射为服务的方法要求入参有三个（第一个默认为调用该方法的实例），返回值只有一个
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// 方法的返回值必须为error类型
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argType, replyType := mType.In(1), mType.In(2)
		// 两个入参，均为导出或内置类型
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		// 第二个入参必须为指针
		if replyType.Kind() != reflect.Ptr {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	// t.PkgPath()返回定义类型的包路径，对于内置类型返回的是空字符串
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
