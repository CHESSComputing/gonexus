package main

import "fmt"

func errRequiredEnv(name string) error {
	return fmt.Errorf("required environment variable %s is not set", name)
}

// apiError is a handler-level error carrying the HTTP status code it
// should be reported with.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(format string, args ...interface{}) *apiError {
	return &apiError{status: 400, msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...interface{}) *apiError {
	return &apiError{status: 404, msg: fmt.Sprintf(format, args...)}
}

func internal(format string, args ...interface{}) *apiError {
	return &apiError{status: 500, msg: fmt.Sprintf(format, args...)}
}
