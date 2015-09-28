package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"git.my-sign.com/backend/coreapi/utils"
	"github.com/mitchellh/mapstructure"
	"github.com/wayt/happyngine/env"
	"github.com/wayt/happyngine/log"
	"gopkg.in/redis.v3"
	"io"
	"os"
	"reflect"
	"runtime"
	"time"
)

var redisCli *redis.Client
var scheduledTasksKey = "scheduled_tasks" // Tasks pushed by the cli, waiting to be push in todo
var todoTasksKey = "todo_tasks"           // Tasks pushed by the scheduler, waiting to be executed
var tasks map[string]*Task
var scheduledTasks *utils.LCFifo
var logger io.Writer = os.Stdout

func init() {

	poolSize := env.GetInt("HAPPY_REDIS_TASK_POOL_SIZE")
	if poolSize <= 0 {
		poolSize = 10
	}

	poolTimeout := time.Duration(env.GetInt("HAPPY_REDIS_TASK_POOL_TIMEOUT")) * time.Millisecond
	if poolTimeout <= 0 {
		poolTimeout = time.Second * 5
	}

	if env.Get("REDIS_TASK_PORT_6379_TCP_ADDR") == "" && env.Get("REDIS_TASK_PORT_6379_TCP_PORT") == "" {
		log.Warningln("Unconfigured redis task...")
		return
	}

	redisCli = redis.NewClient(&redis.Options{
		Addr:        env.Get("REDIS_TASK_PORT_6379_TCP_ADDR") + ":" + env.Get("REDIS_TASK_PORT_6379_TCP_PORT"),
		Password:    env.Get("HAPPY_REDIS_TASK_PASSWORD"),
		DB:          int64(env.GetInt("HAPPY_REDIS_TASK_DB")),
		PoolSize:    poolSize,
		PoolTimeout: poolTimeout,
	})

	taskLogFile := env.Get("TASK_LOG_FILE")
	if taskLogFile != "" {
		f, err := os.OpenFile(taskLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			panic(err)
		}
		// defer f.Close()

		logger = f
	}

	tasks = make(map[string]*Task)
	scheduledTasks = utils.NewListCFifo()

	taskRunnerThreads := env.GetInt("HAPPY_TASK_RUNNER_THREADS")
	if taskRunnerThreads == 0 {
		taskRunnerThreads = runtime.NumCPU()
	}

	for i := 0; i < taskRunnerThreads; i++ {
		go taskRunner()
	}

	go taskScheduler()
}

type Task struct {
	Name string
	fv   reflect.Value // Kind() == reflect.Func
}

func New(name string, i interface{}) *Task {

	if _, ok := tasks[name]; ok {
		panic(errors.New("duplicate task name: " + name))
	}

	t := &Task{
		Name: name,
		fv:   reflect.ValueOf(i),
	}

	f := t.fv.Type()
	if f.Kind() != reflect.Func {
		panic(errors.New("not a function"))
	}

	tasks[name] = t

	return t
}

type TaskSchedule struct {
	Name string
	Args []interface{}
	Time time.Time
}

func (ts *TaskSchedule) MarshalBinary() ([]byte, error) {
	return json.Marshal(ts)
}

func (ts *TaskSchedule) UnmarshalBinary(data []byte) error {

	return json.Unmarshal(data, &ts)
}

func (t *Task) Schedule(tm time.Time, args ...interface{}) {

	utc := tm.UTC()

	sc := &TaskSchedule{
		Name: t.Name,
		Args: args,
		Time: utc,
	}

	scheduledTasks.Enqueue(sc)
}

func (t *Task) call(args ...interface{}) error {

	ft := t.fv.Type()
	in := []reflect.Value{}
	for i, arg := range args {
		var v reflect.Value
		if arg != nil {

			paramType := ft.In(i)

			tmp := reflect.New(paramType)
			mapstructure.Decode(arg, tmp.Interface())

			v = tmp.Elem()
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

	t.fv.Call(in)

	return nil
}

func taskRunner() {
	for {

		taskName := ""
		var startTime time.Time
		status := 200

		func() {

			defer func() {
				if err := recover(); err != nil {

					trace := make([]byte, 1024)
					runtime.Stack(trace, true)

					log.Criticalln("TASK:", err, string(trace))
					status = 500
				}
			}()

			task, err := redisCli.BLPop(0, todoTasksKey).Result()
			if err != nil {
				log.Errorln("TASK: redisCli.BLPop:", err)
				time.Sleep(1 * time.Second)
				return
			}

			ts := &TaskSchedule{}
			if err := ts.UnmarshalBinary([]byte(task[1])); err != nil {
				log.Errorln("TASK: UnmarshalBinary:", err)
				return
			}

			taskName = ts.Name

			t, ok := tasks[ts.Name]
			if !ok {
				log.Errorln("TASK: unknown task:", ts.Name)
				return
			}

			log.Debugln("TASK: running:", ts.Name)
			startTime = time.Now()
			t.call(ts.Args...)
		}()

		took := time.Since(startTime)

		if _, err := fmt.Fprintf(logger, "%s [%s] %d %d\n", taskName, time.Now().Format("2/Jan/2006:15:04:05 -0700"), status, took.Nanoseconds()/1000000); err != nil {
			log.Errorln("taskRunner:", err)
		}
	}
}

func taskScheduler() {
	for true {

		i, ok := scheduledTasks.Dequeue()
		if !ok {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		task := i.(*TaskSchedule)

		timestamp := task.Time.Unix()

		if err := redisCli.ZAdd(scheduledTasksKey, redis.Z{
			Score:  float64(timestamp),
			Member: task,
		}).Err(); err != nil {
			log.Errorln("TASK: redisCli.ZAdd:", task.Name, timestamp, err)

			scheduledTasks.Enqueue(task)
			time.Sleep(500 * time.Millisecond)
		}
	}
}
