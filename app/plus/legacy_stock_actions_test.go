package plus

// A régi általános készletműveletek kizárólag a korábbi hálózati és
// konkurenciatesztekben élnek tovább. A Wails termék API-jában nincsenek jelen.
func (a *ItemWindowApp) AddTrackedItem(uid string) ItemState {
	return a.addTrackedItem(uid, nil)
}

func (a *ItemWindowApp) RemoveTrackedItem(uid string) ItemState {
	return a.removeTrackedItem(uid, nil)
}
