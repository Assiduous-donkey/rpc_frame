# geektutu ———— RPC框架GeeRPC学习

## 来源

![GeeRPC](https://geektutu.com/post/geerpc.html)

## RPC框架需要解决的问题

1. 传输协议

2. 报文编码格式

3. 注册中心：客户端和服务端互相不感知对方的存在

4. 负载均衡

5. 超时处理等“业务之外”的功能

## 关于GeeRPC

从零实现go语言标准库net/rpc，并在此基础上新增协议交换、注册中心、服务发现、负载均衡、超时处理等特性。理解RPC框架在设计时需要考虑的问题。
