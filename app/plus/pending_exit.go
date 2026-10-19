package plus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// PendingExitGuard csak a futó folyamatban él. A jóváhagyás után új Add nem
// fogadható el, így a nulla függő művelet ellenőrzése és a kilépés nem versenyez.
type PendingExitGuard struct {
	mu       sync.Mutex
	approved atomic.Bool
	dialog   func(context.Context, wailsRuntime.MessageDialogOptions) (string, error)
}

func (g *PendingExitGuard) Approved() bool { return g.approved.Load() }

// BeforeClose true értéke megakadályozza a Wails kilépését. A prepare(false)
// csak nulla függő darabnál engedélyez, prepare(true) explicit eldobást jelent.
func (g *PendingExitGuard) BeforeClose(ctx context.Context, prepare func(bool) (int, bool, error)) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.approved.Load() {
		return false
	}
	dialog := g.dialog
	if dialog == nil {
		dialog = wailsRuntime.MessageDialog
	}
	count, approved, err := prepare(false)
	if err == nil && approved {
		g.approved.Store(true)
		return false
	}
	if err != nil || count <= 0 {
		_, _ = dialog(ctx, wailsRuntime.MessageDialogOptions{Type: wailsRuntime.WarningDialog, Title: "Tarkov Market saves", Message: "Pending Tarkov Market saves could not be checked. The application will stay open. Please try closing again."})
		return true
	}
	message := fmt.Sprintf("%d items are still waiting to be saved to Tarkov Market.", count)
	if count == 1 {
		message = "1 item is still waiting to be saved to Tarkov Market."
	}
	choice, err := dialog(ctx, wailsRuntime.MessageDialogOptions{Type: wailsRuntime.QuestionDialog, Title: "Pending Tarkov Market saves", Message: message + "\n\nExit now and discard these pending changes?\n\nChoose No to keep the app open and wait for the saves.", Buttons: []string{"Yes", "No"}, DefaultButton: "No", CancelButton: "No"})
	if err != nil || !strings.EqualFold(choice, "Yes") {
		return true
	}
	_, approved, err = prepare(true)
	if err != nil || !approved {
		return true
	}
	g.approved.Store(true)
	return false
}

func (a *ItemWindowApp) pendingExitApproved() bool { return a.quitGuard.Approved() }

func (a *ItemWindowApp) preparePendingQuit(discard bool) (int, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := a.pendingAddCountLocked()
	if a.quitGuard.approved.Load() {
		return count, true, nil
	}
	if count > 0 && !discard {
		return count, false, nil
	}
	a.quitGuard.approved.Store(true)
	return count, true, nil
}

func (a *ItemWindowApp) cancelPendingQuitPreparation() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.quitGuard.approved.Store(false)
}

func (a *ItemWindowApp) beforeClose(ctx context.Context) bool {
	return a.quitGuard.BeforeClose(ctx, a.preparePendingQuit)
}
