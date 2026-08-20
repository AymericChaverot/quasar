package server

import (
	"encoding/json"
	"errors"

	"quasar/internal/db"
)

// The store capabilities: a key-value space scoped to one station on one
// application, and the only thing a station can reach without asking for it.
//
// Dispatched from Do in station_broker.go, which is where what every
// capability has in common is set out.

// storeArgs are the arguments every store call takes some of.
type storeArgs struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// store is the one capability that needs no permission: it is scoped to this
// application and this station, and there is nothing in it a station could
// reach that is not its own.
func (c *stationCall) store(capability string, raw json.RawMessage) (json.RawMessage, error) {
	var a storeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if capability != "store.keys" && a.Key == "" {
		return nil, errors.New("quasar.store needs a key")
	}

	switch capability {
	case "store.get":
		value, ok := db.StationStoreGet(c.srv.db, c.app.ID, c.doc.ID, a.Key)
		if !ok {
			return json.RawMessage("null"), nil
		}
		return json.RawMessage(value), nil

	case "store.set":
		value := string(a.Value)
		if value == "" {
			value = "null"
		}
		if err := db.StationStoreSet(c.srv.db, c.app.ID, c.doc.ID, a.Key, value); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil

	case "store.delete":
		if err := db.StationStoreDelete(c.srv.db, c.app.ID, c.doc.ID, a.Key); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil
	}

	keys, err := db.StationStoreKeys(c.srv.db, c.app.ID, c.doc.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(keys)
}
