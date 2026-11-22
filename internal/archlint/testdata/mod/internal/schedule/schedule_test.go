package schedule

import (
	"os"
	"time"
)

var (
	_ = os.Getenv
	_ = time.Sleep // want notime
)
