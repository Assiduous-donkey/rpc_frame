# geektutu ———— RPC框架GeeRPC学习

## 服务注册

服务注册指的是将结构体的方法映射为服务使得可以通过网络进行调用。

### 结构体映射为服务

对于```net/rpc```而言，一个函数可以被远程调用需要满足以下条件：

1. 函数所属类型是导出的（即可被非同一个package的程序调用）
2. 函数是导出的
3. 函数拥有两个入参且均为导出或内置类型
4. 第二个入参必须为指针
5. 函数只有一个返回值且为error类型

```go
    func (t *T) MethodName(argType T1, replyType *T2) error
```

客户端发送的请求包含了```ServiceMethod```，记录了请求要调用的结构体及其方法名。通过**反射**获取该结构体的所有方法以及方法的入参和返回值，再决定如何在服务端完成调用。

```go
    type methodType struct { // 存储一个方法的完整信息
        method    reflect.Method
        ArgType   reflect.Type
        ReplyType reflect.Type
    }
```

Go的反射提供了三种结构体Method、Type和Value，代表了方法、类型和值，并且包含了获取它们详细信息的接口，比如获取一个方法的参数类型、获取一个类型的名称等等。

```go
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
```

```newArgv()```方法和```newReplyv()```方法用于根据类型创建对应的参数和响应的实例，用于解码客户端请求的参数信息以及编码服务端返回的响应信息。

1. ```reflect.New(Type) Value```：根据类型创建一个反射的值实例指针，相当于在平常的使用中我们有时会```new(int)```创建一个int类型的指针。
2. ```Type.Elem()```：当Type为指针时，Elem()用于获取指针指向的元素类型
3. ```Type.Kind()```：用于获取类型所属的大类，比如指针为一个大类```reflect.Ptr```，结构体为一个大类```reflect.Struct```
4. ```Value.Set(Value)```：是Value直接的赋值操作，在上面的```newReplyv()```方法中被用于为map和slice类型的实例申请内存空间后赋值。

```go
    type service struct {
        name   string                 // 映射的结构体名称
        typ    reflect.Type           // 结构体类型
        rcvr   reflect.Value          // 结构体的实例本身
        method map[string]*methodType // 存储结构体所有可以映射为服务的方法
    }
```

一个结构体对应一个服务，由于调用结构体方法时第一个参数（隐藏参数）为结构体实例本身，所以需要存储结构体实例本身```rcvr```。为了通过方法名查找方法然后调用，需要一个map来记录方法名和方法的映射关系。

```go
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
```

创建一个服务的操作实际上就是将一个结构体映射为服务，所以需要传递结构体实例```rcvr```进行构造。```registerMethods()```方法用于解析结构体包含的可被远程调用的方法

```go
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
```

通过上述反射方法获取结构体的所有方法并检查是否满足可被远程调用的条件。

1. ```Type.NumMethod()```：获取结构体的方法数量
2. ```Type.Method(i)```：获取结构体的第i个方法
3. ```Type.NumIn()```：Type为函数类型时获取该函数的入参数量，若函数为某个类型的方法则第一个参数为该类型的一个实例
4. ```Type.NumOut()```：Type为函数类型时获取该函数的返回值数量
5. ```Type.Out(i) Type```：获取函数的第i个返回值的类型（从0开始）
6. ```Type.In(i) Type```：获取函数的第i个入参的类型（从0开始）

```go
    func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
        f := m.method.Func
        returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
        if errInter := returnValues[0].Interface(); errInter != nil {
            return errInter.(error)
        }
        return nil
    }
```

通过反射值调用方法。```Method.Func```是该方法的函数实例，也是一个```reflect.Value```类型，第一个参数必须为方法所属类型的实例。调用```Value.Call```方法执行函数。由于```reflect.Value```类型没有直接转为```error```的结构所以先转为```interface```类型然后再转为```error```。

### 服务端上注册服务

```go
    type Server struct {
        serviceMap sync.Map // 存储结构体名以及对应的service对象
    }

    // 向server注册提供服务的结构体
    func (server *Server) Register(rcvr interface{}) error {
        s := newService(rcvr)
        if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
            return errors.New("rpc: service already defined: " + s.name)
        }
        return nil
    }

    func Register(rcvr interface{}) error {
        return DefaultServer.Register(rcvr)
    }
```

服务端提供一个线程安全的Map存储注册为服务的对象名以及对应的service对象。并提供对外的注册服务的接口，接受对象实例作为入参。

```go
    // 通过RPC调用的入参Service.Method解析得到对应的服务和方法
    func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
        dot := strings.LastIndex(serviceMethod, ".")
        if dot < 0 {
            err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
            return
        }
        serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
        svci, ok := server.serviceMap.Load(serviceName)
        if !ok {
            err = errors.New("rpc server: can't find service " + serviceName)
            return
        }
        svc = svci.(*service)
        mtype = svc.method[methodName]
        if mtype == nil {
            err = errors.New("rpc server: can't find method " + methodName)
        }
        return
    }
```

服务端需要通过请求包含的ServiceMethod来确定要调用的方法。ServiceMethod的格式为“Service.Method”，据此获取服务名和方法名检查是否存在。

```go
    type request struct {
        h            *codec.Header
        argv, replyv reflect.Value // 类型需要通过反射在运行时确定
        mtype        *methodType   // 请求调用的方法信息
        svc          *service      // 请求调用的服务——结构体信息
    }
```

request作为辅助服务端处理请求的内部结构体，需要记录请求调用的服务信息和对应的方法信息。

```go
    func (server *Server) readRequest(cc codec.Codec) (*request, error) {
        h, err := server.readRequestHeader(cc)
        if err != nil {
            return nil, err
        }
        // 构建完整的请求消息结构
        req := &request{h: h}
        req.svc, req.mtype, err = server.findService(h.ServiceMethod)
        if err != nil {
            return req, err
        }
        req.argv = req.mtype.newArgv()
        req.replyv = req.mtype.newReplyv()

        argvi := req.argv.Interface() // argvi必须为指针类型才能通过codec.ReadBody方法获取报文正文信息
        if req.argv.Type().Kind() != reflect.Ptr {
            argvi = req.argv.Addr().Interface()
        }
        if err = cc.ReadBody(argvi); err != nil {
            log.Println("rpc server: read argv err: ", err)
            return req, err
        }
        return req, nil
    }
```

在读取请求信息时首先读请求头获取ServiceMethod然后据此查找服务信息和方法信息，再通过方法的参数和响应类型创建对应的实例。参数实例用于读取请求报文的正文。

```go
    func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
        // 等待同一个连接的多个请求处理完再关闭连接
        defer wg.Done()

        err := req.svc.call(req.mtype, req.argv, req.replyv) // 调用RPC方法
        if err != nil {
            req.h.Error = err.Error()
            server.sendResponse(cc, req.h, invalidRequest, sending)
            return
        }

        server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
    }
```

请求的处理直接调用了服务的call方法。

### 与用户交互

用户通过```geerpc.Register(对象实例)```将对象注册为可提供RPC服务的对象，然后监听指定的通信端口。客户端向服务端的监听端口建立连接后，通过客户端的Call方法进行RPC调用。
