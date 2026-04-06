package main

import "testing"

func TestMainExitsWithExecuteCode(t *testing.T) {
	oldExecute := execute
	oldExit := exitFunc
	t.Cleanup(func() {
		execute = oldExecute
		exitFunc = oldExit
	})

	code := -1
	execute = func() int { return 7 }
	exitFunc = func(c int) { code = c }

	main()

	if code != 7 {
		t.Fatalf("exit code: want 7 got %d", code)
	}
}
