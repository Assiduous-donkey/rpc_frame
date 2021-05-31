package geerpc

import (
	"fmt"
	"html/template"
	"net/http"
)

const debugText = `<html>
	<body>
	<title>GeeRPC Services</title>
	{{range .}}
	<hr>
	Service {{.Name}}
	<hr>
		<table>
		<th align=center> Method</th><th align=center> Calls</th>
		{{range $name, $mtype := .Method}}
			<tr>
			<td align=left front=fixed>{{$name}}({{$mtype.ArgType}}, {{$mtype.ReplyType}}) error</td>
			<td align=center>{{$mtype.NumCalls}}</td>
			</tr>
		{{end}}
		</table>
	{{end}}
	</body>
	</html>`

var debug = template.Must(template.New("RPC debug").Parse(debugText))

type debugHTTP struct {
	*Server
}

type debugService struct {
	Name   string
	Method map[string]*methodType
}

func (server debugHTTP) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var services []debugService
	server.serviceMap.Range(func(namei, svci interface{}) bool { // Range用于遍历syncMap并对每个键值对执行作为参数的函数
		svc := svci.(*service)
		services = append(services, debugService{
			Name:   namei.(string),
			Method: svc.method,
		})
		return true
	})
	err := debug.Execute(w, services)
	if err != nil {
		_, _ = fmt.Fprintln(w, "rpc: error executing template: ", err.Error())
	}
}
