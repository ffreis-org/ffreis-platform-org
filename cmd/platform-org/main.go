package main

import (
	"os"

	"github.com/ffreis/platform-org/cmd"
)

var execute = cmd.Execute
var exitFunc = os.Exit

func main() {
	exitFunc(execute())
}
