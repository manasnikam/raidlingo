package raidlingo

import "os/exec"

// CommandContext cancels the direct child on Windows. Go's WaitDelay also
// bounds waiting for inherited pipes; macOS/Linux cancel the process group.
func isolateCodexProcess(cmd *exec.Cmd) {}
