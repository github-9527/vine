package template

var (
	Module = `module {{.Dir}}

go {{.GoVersion}}

require (
	github.com/vine-io/vine {{.VineVersion}}
)

replace google.golang.org/grpc => google.golang.org/grpc v1.38.0
`
)
