package main

import "golang.zx2c4.com/wintun"

func test() {
    var session wintun.Session
    handle := session.ReadWaitEvent()
    _ = handle
}
