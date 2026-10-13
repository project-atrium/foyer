//go:build !unix

package copies

import "os/exec"

func free(string) int64 { return -1 }

func lower(*exec.Cmd) {}

func killed(error) bool { return false }
