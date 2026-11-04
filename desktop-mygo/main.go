package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"wtable/internal/cli"
)

func main() {
	// The daemon puts this executable in WTABLE_CLI for its agents. Every
	// command shares the implementation of the standalone CLI.
	if len(os.Args) > 1 {
		if err := cli.Run(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "wtable:", err)
			os.Exit(1)
		}
		return
	}
	workbench := newWorkbench()
	mygo.App.SetName("wtable")
	mygo.App.WhenReady(func() {
		workbench.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "wtable", Width: 1440, Height: 900,
			MinWidth: 1120, MinHeight: 720, StateKey: "workbench",
			TitleBarStyle: mygo.TitleBarHidden,
			Content:       ui.View(workbench.view),
		})
		workbench.win.OnClosed(func() {
			workbench.save()
			workbench.cancel()
		})
		workbench.start()
	})
	err := mygo.App.Run()
	workbench.stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wtable desktop:", err)
		os.Exit(1)
	}
}
