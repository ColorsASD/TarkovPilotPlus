package main

// OpenItemHandover a főablak leadási gombját kapcsolja a társablakhoz.
func (a *App) OpenItemHandover() bool {
	return a != nil && a.companion != nil && a.companion.OpenHandover()
}
