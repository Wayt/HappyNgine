package happyngine

import (
	"encoding/json"
	"github.com/wayt/happyngine/env"
	"github.com/wayt/happyngine/log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var Hostname = ""

func init() {

	var err error
	Hostname, err = os.Hostname()
	if err != nil || Hostname == "" {
		Hostname = env.Get("NODE_NAME")
	}
}

type Context struct {
	Request            *http.Request       `json:"-"`
	Response           http.ResponseWriter `json:"-"`
	API                *API                `json:"-"`
	Session            *Session            `json:"-"`
	ResponseStatusCode int                 `json:"-"` // Because we can't retrieve the status from http.ResponseWriter
	ResponseLength     int                 `json:"-"`
	Errors             map[string]string   `json:"-"`
	ErrorCode          int                 `json:"-"`
	currentMiddleware  int
	middlewares        []MiddlewareHandler
	action             ActionHandler
}

func NewContext(req *http.Request, resp http.ResponseWriter, api *API) *Context {

	c := new(Context)

	c.Request = req
	c.Response = resp
	c.API = api
	c.ResponseStatusCode = 200
	c.Errors = make(map[string]string)
	c.currentMiddleware = -1

	return c
}

func (c *Context) Next() {

	c.currentMiddleware += 1

	if len(c.middlewares) <= c.currentMiddleware {

		// Process action
		action := c.action(c)

		if action.IsValid() {

			action.Run()
		}

	} else {
		next := c.middlewares[c.currentMiddleware]
		next(c)
	}

	if c.ResponseLength == 0 {
		errors, code := c.GetErrors()
		if len(errors) != 0 {

			response := `{"error":["` + strings.Join(errors, `","`) + `"]}`
			c.Send(code, response)
		}
	}
}

// Session may be nil
func (c *Context) FetchSession(name string) *Session {

	sess := GetSession(c.Request, name)
	c.Session = sess

	return sess
}

func (c *Context) NewSession(name string) *Session {

	secure := env.Get("SECURE_COOKIE")

	c.Session = NewSession(name, &SessionOptions{
		Path:     "/",
		MaxAge:   env.GetInt("SESSION_MAX_AGE"),
		HttpOnly: true,
		Secure:   secure != "false",
	})

	return c.Session
}

func (c *Context) GetParam(key string) string {

	return c.Request.FormValue(key)
}

func (c *Context) GetIntParam(key string) int {

	value, err := strconv.Atoi(c.Request.FormValue(key))
	if err != nil {
		return 0
	}

	return value
}

func (c *Context) GetInt64Param(key string) int64 {

	value, err := strconv.ParseInt(c.Request.FormValue(key), 10, 64)
	if err != nil {
		return 0
	}

	return value
}

func (c *Context) GetURLParam(key string) string {

	return c.Request.URL.Query().Get(key)
}

func (c *Context) GetURLIntParam(key string) int {

	value, err := strconv.Atoi(c.GetURLParam(key))
	if err != nil {
		return 0
	}
	return value
}

func (c *Context) GetURLInt64Param(key string) int64 {

	value, err := strconv.ParseInt(c.GetURLParam(key), 10, 64)
	if err != nil {
		return 0
	}

	return value
}

func (c *Context) JSON(code int, obj interface{}, headers ...string) {

	data, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	c.SendByte(code, data, append(headers, "Content-Type: application/json")...)
}

func (c *Context) Send(code int, text string, headers ...string) {
	c.SendByte(code, []byte(text), headers...)
}

func (c *Context) SendByte(code int, data []byte, headers ...string) {

	hasMime := false
	for _, header := range headers {

		array := strings.Split(header, ":")
		if len(array) != 2 {
			continue
		}

		c.Response.Header().Add(array[0], array[1])

		if array[0] == "Content-Type" {
			hasMime = true
		}
	}

	if Hostname != "" {
		c.Response.Header().Add("X-happyngine-node", Hostname)
	}

	if !hasMime {
		c.Response.Header().Add("Content-Type", "application/json")
	}

	for k, v := range c.API.Headers {

		matchs := regexp.MustCompile(`^{(.*)}$`).FindStringSubmatch(v)
		if len(matchs) != 0 {
			header := matchs[1]
			if v = c.Request.Header.Get(header); len(v) == 0 {
				continue
			}
		}

		c.Response.Header().Add(k, v)
	}

	if c.Session != nil {
		if c.Session.Changed() {
			c.Session.Save(c.Request, c.Response)
		}
	}

	c.Response.WriteHeader(code)
	c.ResponseLength, _ = c.Response.Write(data)
	c.ResponseStatusCode = code
}

func (c *Context) RemoteIP() string {

	ipStr := strings.SplitN(c.Request.RemoteAddr, ":", 2)[0]

	if header := c.Request.Header.Get("X-Forwarded-For"); len(header) != 0 {
		// Because of google http load balancer
		// X-Forwarded-For: <client IP(s)>, <global forwarding rule external IP> (requests only)
		ipStr = strings.Split(header, ",")[0]
	}

	return strings.Trim(ipStr, " ")
}

func (c *Context) Debugln(args ...interface{}) {
	log.Debugln(args)
}

func (c *Context) Warningln(args ...interface{}) {
	log.Warningln(args)
}

func (c *Context) Errorln(args ...interface{}) {
	log.Errorln(args)
}

func (c *Context) Criticalln(args ...interface{}) {
	log.Criticalln(args)
}

func (c *Context) AddError(code int, text string) {
	c.ErrorCode = code
	c.Errors[text] = text
}

func (c *Context) GetErrors() ([]string, int) {

	errs := make([]string, 0)
	for _, err := range c.Errors {
		errs = append(errs, err)
	}

	return errs, c.ErrorCode
}

func (c *Context) HasErrors() bool {
	return len(c.Errors) != 0
}
