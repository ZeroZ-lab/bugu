package schedule

import (
	"os" // want purecore
	t2 "time"
)

var X = 1

var (
	_ = os.Getenv
	_ = t2.Now() // want notime
	_ = t2.Since // want notime
	_ = t2.Duration(0).String()
)
