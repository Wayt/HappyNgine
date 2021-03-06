package async

import (
	"errors"
	"fmt"
	"github.com/wayt/happyngine"
	"github.com/wayt/happyngine/log"
	"reflect"
	"runtime"
)

var (
	// precomputed types
	contextType = reflect.TypeOf((*happyngine.Context)(nil))
)

type Function struct {
	Name string
	fv   reflect.Value // Kind() == reflect.Func
}

type FunctionResult struct {
	errs   chan error
	result []reflect.Value
}

func New(name string, i interface{}) *Function {

	f := &Function{
		Name: name,
		fv:   reflect.ValueOf(i),
	}

	t := f.fv.Type()
	if t.Kind() != reflect.Func {
		panic(errors.New("not a function"))
	}
	if t.NumIn() == 0 || t.In(0) != contextType {
		panic(errors.New("first argument must be *happyngine.Context"))
	}

	return f
}

func (f *Function) Call(c *happyngine.Context, args ...interface{}) *FunctionResult {

	ft := f.fv.Type()
	in := []reflect.Value{reflect.ValueOf(c)}
	for _, arg := range args {
		var v reflect.Value
		if arg != nil {
			v = reflect.ValueOf(arg)
		} else {
			// Task was passed a nil argument, so we must construct
			// the zero value for the argument here.
			n := len(in) // we're constructing the nth argument
			var at reflect.Type
			if !ft.IsVariadic() || n < ft.NumIn()-1 {
				at = ft.In(n)
			} else {
				at = ft.In(ft.NumIn() - 1).Elem()
			}
			v = reflect.Zero(at)
		}
		in = append(in, v)
	}

	result := new(FunctionResult)
	result.errs = make(chan error)

	go func() {
		defer close(result.errs)

		// Panic handler
		defer RecoverOnPanic(f.Name)
		result.result = f.fv.Call(in)
	}()

	return result
}

func (r *FunctionResult) Wait() []reflect.Value {

	<-r.errs

	return r.result
}

func RecoverOnPanic(name string) {
	if r := recover(); r != nil {

		log.Criticalln("happyngine.Async.Function.Call: "+name+":", r)

		trace := make([]byte, 1024)
		runtime.Stack(trace, true)

		log.Criticalln(r, string(trace))
	}
}

func CatchPanic(errs chan error) {

	if r := recover(); r != nil {
		errs <- errors.New(fmt.Sprintf("%v", r))
	}
}
