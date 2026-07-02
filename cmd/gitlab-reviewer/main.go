package main

import (
	"os"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
