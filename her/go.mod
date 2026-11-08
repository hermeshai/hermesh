// her/ is the her kernel's Rust workspace, with no Go code. This file keeps the repository's
// Go tools out of it and its target/ folder: ./... stops at a folder with a go.mod of its own.
module github.com/hermeshai/hermesh/her

go 1.27.1
