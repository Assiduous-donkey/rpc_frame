# geektutu ———— RPC框架GeeRPC学习

## 服务发现与注册中心

有了注册中心之后，客户端和服务端只需要感知注册中心的存在即可。
服务端启动后向注册中心发送注册消息，注册中心得知该服务已经启动，处于可用状态。一般来说，服务端还需要定期向注册中心发送心跳证明自己仍然有效。
客户端向注册中心查询可用的服务实例，注册中心会将所有可用的服务实例返回给客户端，由客户端根据自身的负载均衡策略选择一个实例进行调用。

### 简单的支持心跳的注册中心

```go
    /* 注册中心 */
    type GeeRegistry struct {
        timeout time.Duration // 服务超时时间 默认为5分钟
        mu      sync.Mutex
        servers map[string]*ServerItem
    }

    type ServerItem struct { // 服务实例
        Addr  string
        start time.Time // 用于检查服务是否超时
    }

    const (
        defaultPath    = "/_geerpc_/registry"
        defaultTimeout = time.Minute * 5
    )
```

一个注册中心```GeeRegistry```包括了表示服务超时时间的```timeout```，超时后服务不可用除非再次注册到注册中心。```ServerItem.start```用于记录服务实例注册到注册中心的时间。注册中心通过HTTP协议与服务端和客户端交互，采用```/_geerpc_/registry```作为URL。

```go
    // 添加服务实例
    func (r *GeeRegistry) putServer(addr string) {
        r.mu.Lock()
        defer r.mu.Unlock()
        s := r.servers[addr]
        if s == nil {
            r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
        } else {
            s.start = time.Now()    // 重置注册时间
        }
    }

    // 返回可用的服务列表
    func (r *GeeRegistry) aliveServers() []string {
        r.mu.Lock()
        defer r.mu.Unlock()
        var alive []string
        for addr, s := range r.servers {
            // r.timeout = 0 表示不限制服务超时时间
            if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
                alive = append(alive, addr)
            } else {
                delete(r.servers, addr)
            }
        }
        sort.Strings(alive) // 这里排序是为了让客户端实现有效的负载均衡 比如轮询
        return alive
    }
```

注册中心有两个基础的操作：添加服务实例和返回可用的服务列表。通过```s.start.Add(r.timeout).After(time.Now())```检查是否注册超时。获取可用服务列表后对服务实例的地址做了排序处理，是为了在可用服务实例不变的情况下保证每次返回的[]string中元素的值和顺序一样。保证客户端获取到可用的服务实例后进行的负载均衡选择可靠。

```go
    func (r *GeeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
        switch req.Method {
        case "GET": // GET返回所有可用的服务实例列表 承载在HTTP Header中 —— X-Geerpc-Servers
            w.Header().Set("X-Geerpc-Servers", strings.Join(r.aliveServers(), ","))
        case "POST": // 添加服务实例或发送心跳 承载在HTTP Header中 —— X-Geerpc-Server
            addr := req.Header.Get("X-Geerpc-Server")
            if addr == "" {
                w.WriteHeader(http.StatusInternalServerError)
                return
            }
            r.putServer(addr)
        default:
            w.WriteHeader(http.StatusMethodNotAllowed)
        }
    }
```

采用HTTP协议提供服务，所有有用的信息都承载在HTTP Header中。HTTP Header中的信息以键值对的形式。

1. GET方法：返回所有可用的服务列表，通过自定义字段X-Geerpc-Servers承载
2. POST方法：添加服务实例或发送心跳，通过自定义字段X-Geerpc-Server承载

```go
    // 便于服务启动定时向注册中心发送心跳 默认周期比注册中心设置的过期时间少1min
    func Heartbeat(registry, addr string, duration time.Duration) {
        if duration == 0 {
            duration = defaultTimeout - time.Duration(1)*time.Minute
        }
        var err error
        err = sendHeartbeat(registry, addr)
        go func() {
            t := time.NewTicker(duration) // 定时计时器
            for err == nil {
                <-t.C
                err = sendHeartbeat(registry, addr)
            }
        }()
    }

    func sendHeartbeat(registry, addr string) error {
        log.Println(addr, "send heart beat to registry", registry)
        httpClient := &http.Client{}
        req, _ := http.NewRequest("POST", registry, nil)
        req.Header.Set("X-Geerpc-Server", addr)
        // http.Client.Do：发现HTTP请求并返回一个HTTP响应
        if _, err := httpClient.Do(req); err != nil {
            log.Println("rpc server: heat beat err:", err)
            return err
        }
        return nil
    }
```

注册中心提供```Heartbeat```方法供服务实例调用，其中开启一个goroutine定时调用```sendHeartbeat```向注册中心发起注册服务的请求。

### 基于注册中心的服务发现

```go
    /* 基于注册中心的服务发现 */
    type GeeRegistryDiscovery struct {
        *MultiServerDiscovery
        registry   string        // 注册中心地址
        timeout    time.Duration // 服务实例的过期时间
        lastUpdate time.Time     // 最后从注册中心获取服务列表的时间 默认每隔10s从注册中心获取
    }

    // 手工维护服务列表，不需要注册中心的服务发现结构体
    type MultiServerDiscovery struct {
        r       *rand.Rand // 获取随机数
        mu      sync.RWMutex
        servers []string
        index   int // 用于轮询策略 记录已经轮询到的位置
    }
```

```GeeRegistryDiscovery```是基于注册中心实现服务发现的结构体，继承了之前实现的手工维护服务列表的服务发现结构体，以便于复用一些属性和方法。
```GeeRegistryDiscovery```维护了一个```lastUpdate```属性，表示最后从注册中心获取服务列表的时间，这是一种折中的策略，避免每次客户端指向RPC调用都要从注册中心获取服务实例再进行选举，也防止注册中心中可用的服务实例列表改变。

```go
    func (d *GeeRegistryDiscovery) Update(servers []string) error {
        d.mu.Lock()
        defer d.mu.Unlock()
        d.servers = servers
        d.lastUpdate = time.Now()
        return nil
    }

    func (d *GeeRegistryDiscovery) Refresh() error {
        d.mu.Lock()
        defer d.mu.Unlock()
        if d.lastUpdate.Add(d.timeout).After(time.Now()) { // 未超时
            return nil
        }
        log.Println("rpc registry: refresh servers from registry", d.registry)
        resp, err := http.Get(d.registry)
        if err != nil {
            log.Println("rpc registry refresh err:", err)
            return err
        }
        servers := strings.Split(resp.Header.Get("X-Geerpc-Servers"), ",")
        d.servers = make([]string, 0, len(servers))
        for _, server := range servers {
            if s := strings.TrimSpace(server); s != "" { // TrimSpace去掉字符串首尾的空格
                d.servers = append(d.servers, s)
            }
        }
        d.lastUpdate = time.Now()
        return nil
    }

    // 获取服务实例之前需要先调用Refresh确保实例没过期
    func (d *GeeRegistryDiscovery) Get(mode SelectMode) (string, error) {
        if err := d.Refresh(); err != nil {
            return "", err
        }
        return d.MultiServerDiscovery.Get(mode)
    }

    func (d *GeeRegistryDiscovery) GetAll() ([]string, error) {
        if err := d.Refresh(); err != nil {
            return nil, err
        }
        return d.MultiServerDiscovery.GetAll()
    }
```

作为一个服务发现接口需要实现上面四个方法。
```Refresh```方法首先检查距离上次从注册中心获取服务实例是否超时，是的话则重新获取。通过```http.Get(url)```直接发送HTTP请求。

### 使用方法

```go
    func startRegistry(wg *sync.WaitGroup) {
        l, _ := net.Listen("tcp", ":9999")
        registry.HandleHTTP()
        wg.Done()
        _ = http.Serve(l, nil)
    }
```

注册中心通过开启HTTP服务启动。

```go
    func startServer(registryAddr string, wg *sync.WaitGroup) {
        var foo Foo
        l, err := net.Listen("tcp", ":0")
        if err != nil {
            log.Fatal("network error: ", err)
        }
        server := geerpc.NewServer()
        if err := server.Register(foo); err != nil {
            log.Fatal("register error: ", err)
        }
        log.Println("start rpc server on", l.Addr())
        registry.Heartbeat(registryAddr, "tcp@"+l.Addr().String(), 0)
        wg.Done()
        server.Accept(l)
    }
```

创建服务实例后在服务器注册提供RPC方法的结构体，然后调用注册中心的心跳方法开始向注册中心发送心跳请求，同时注册服务。

```go
    func call(registry string) {
        d := xclient.NewGeeRegistryDiscovery(registry, 0)
        xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
        defer xc.Close()
        var wg sync.WaitGroup
        for i := 0; i < 5; i++ {
            wg.Add(1)
            go func(i int) {
                defer wg.Done()
                var reply int
                err = xc.Call(context.Background(), "Foo.Sum",  &Args{Num1: i, Num2: i * i}, &reply)
            }(i)
        }
        wg.Wait()
    }
```

客户端方面，首先创建一个基于注册中心的服务发现结构体，再用这个结构体创建有负载均衡功能的客户端。之后直接调用```Call``方法进行RPC调用即可。

```go
    func main() {
        registryAddr := "http://localhost:9999/_geerpc_/registry"
        var wg sync.WaitGroup
        wg.Add(1)
        go startRegistry(&wg)
        wg.Wait()

        time.Sleep(time.Second)

        wg.Add(2)
        go startServer(registryAddr, &wg)
        go startServer(registryAddr, &wg)
        wg.Wait()

        time.Sleep(time.Second)
        call(registryAddr)
    }
```

总体运行方式如上，用```WaitGroup```确保注册中心先启动服务，然后服务器再进行注册服务，然后客户端才可以进行RPC调用。
