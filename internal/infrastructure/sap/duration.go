package sap

import "time"

func timeDuration(milliseconds int) time.Duration {
	return time.Duration(milliseconds) * time.Millisecond
}
