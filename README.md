# geektutu ———— RPC框架GeeRPC学习

## 负载均衡与服务发现

负载均衡的前提是有个服务器实例。选择哪个实例进行调用需要基于服务发现模块。

```go
    type SelectMode int // 表示不同的负载均衡策略

    const (
        RandomSelect     SelectMode = iota // 随机选择
        RoundRobinSelect                   // 轮询
    )

    type Discovery interface {
        Refresh() error                      // 从注册中心更新服务列表
        Update(servers []string) error       // 手动更新服务列表
        Get(mode SelectMode) (string, error) // 根据负载均衡策略选择一个服务实例
        GetAll() ([]string, error)           // 获取所有服务实例
    }

    // 手工维护服务列表，不需要注册中心的服务发现结构体
    type MultiServerDiscovery struct {
        r       *rand.Rand // 获取随机数
        mu      sync.RWMutex
        servers []string
        index   int // 用于轮询策略 记录已经轮询到的位置
    }

    func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
        d := &MultiServerDiscovery{
            servers: servers,
            r:       rand.New(rand.NewSource(time.Now().UnixNano())),
        }
        d.index = d.r.Intn(math.MaxInt32 - 1) // 初始化一个随机值 避免每次从0开始
        return d
    }
```

定义一个服务发现的接口类型，以及一个暂时无注册中心的服务发现结构体，人工维护服务列表。
定义了两种简单的负载均衡策略：选举选取和轮询。

服务发现接口的四个方法实现都很简单。

```go
    func (d *MultiServerDiscovery) Refresh() error {
        return nil
    }

    func (d *MultiServerDiscovery) Update(servers []string) error {
        d.mu.Lock()
        defer d.mu.Unlock()
        d.servers = servers // slice非线程安全
        return nil
    }

    func (d *MultiServerDiscovery) Get(mode SelectMode) (string, error) {
        d.mu.RLock() // 并发情况下slice非线程安全
        defer d.mu.RUnlock()

        n := len(d.servers)
        if n == 0 {
            return "", errors.New("rpc discovery: no available servers")
        }
        switch mode {
        case RandomSelect:
            return d.servers[d.r.Intn(n)], nil
        case RoundRobinSelect:
            s := d.servers[d.index%n]
            d.index = (d.index + 1) % n
            return s, nil
        default:
            return "", errors.New("rpc discovery: not supported select mode")
        }
    }

    func (d *MultiServerDiscovery) GetAll() ([]string, error) {
        d.mu.RLock()
        defer d.mu.RUnlock()

        servers := make([]string, len(d.servers), len(d.servers))
        copy(servers, d.servers)
        return servers, nil
    }
```

因为暂时没设计注册中心，所以没有实现```Refresh```方法的具体逻辑。

### 客户端的设计

```go
    type XClient struct { // 支持负载均衡的客户端	———— 复用了geerpc.Client的功能
        d       Discovery
        mode    SelectMode
        opt     *Option
        mu      sync.Mutex
        clients map[string]*Client // 复用已创建好的连接 实例地址 - 对应的客户端
    }
```

一个支持负载均衡的客户端需要维护负载均衡策略以及每个实例对应的客户端，以便于复用。

```go
    // 创建客户端
    func (xc *XClient) dial(rpcAddr string) (*Client, error) {
        xc.mu.Lock()
        defer xc.mu.Unlock()

        client, ok := xc.clients[rpcAddr]
        if ok && !client.IsAvailable() { // 客户端不可用
            _ = client.Close()
            delete(xc.clients, rpcAddr)
            client = nil
        }
        if client == nil {
            var err error
            client, err = XDial(rpcAddr, xc.opt)
            if err != nil {
                return nil, err
            }
            xc.clients[rpcAddr] = client
        }
        return client, nil
    }

    func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
        client, err := xc.dial(rpcAddr)
        if err != nil {
            return err
        }
        return client.Call(ctx, serviceMethod, args, reply)
    }

    func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
        rpcAddr, err := xc.d.Get(xc.mode)
        if err != nil {
            return err
        }
        return xc.call(rpcAddr, ctx, serviceMethod, args, reply)
    }
```

通过```XClient.dial```方法创建连接指定地址的RPC客户端，复用了```geerpc.Client```。当客户端不可用时调用创建```geerpc.Client```的方法创建新客户端并存储在```XClient```中。

```go
    // 并发向所有实例调用指定的方法
    func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
        servers, err := xc.d.GetAll()
        if err != nil {
            return err
        }

        var wg sync.WaitGroup
        var mu sync.Mutex
        var e error
        replyDone := reply == nil

        ctx, cancel := context.WithCancel(ctx) // 借助 context.WithCancel 确保有错误发生时，快速通知其他使用了context的实例
        for _, rpcAddr := range servers {
            wg.Add(1)
            go func(rpcAddr string) {
                defer wg.Done()
                var clonedReply interface{}
                if reply != nil {
                    clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
                }
                err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
                mu.Lock()
                if err != nil && e == nil { // 遇到错误则取消其他执行调用
                    e = err
                    cancel()
                }
                if err == nil && !replyDone { // 只返回其中一个成功的调用的返回值
                    reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
                    replyDone = true
                }
                mu.Unlock()
            }(rpcAddr)
        }
        wg.Wait()
        return e
    }
```

为```XClient```添加一个常用的功能```Broadcast```。该方法将同一个请求广播给所有服务器实例，当任意一个实例处理请求出错时返回错误并通过```context.WithCancel```生成的```CancelFunc```函数cancel来通知用了同一个context的其他实例结束处理请求。若请求得到成功处理，则返回任一成功处理的结果。

由于参数```reply```是一个interface{}类型的指针，所以每个实例在执行RPC调用时需要先根据reply构造一个针对该实例的reply：```clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()```。在为reply赋值时也同样采用反射的方式：```reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())```。不能够用```reply = clonedReply```代替。

我的推测是：
如果直接```reply = clonedReply```，由于两个变量都是指针，而clonedReply是在goroutine中定义的，可能clonedReply所指向的空间在goroutine释放时也被释放了，这样做会导致reply最终指向一块未知内容的空间。

```go
    func fc(reply *int) {
        go func() {
            clone := new(int)
            *clone = 5
            reply = clone
        }()
    }

    func main() {
        reply := new(int)
        fc(reply)
        fmt.Println(*reply)
    }
```

上面代码执行结果是打印了0。印证了不可以直接```reply = clonedReply```。
