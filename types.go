package protocolberg

// for unmarshalling finalization data
type finalization struct {
	Data data `json:"data"`
}

type data struct {
	Finalized finalized `json:"finalized"`
}

type finalized struct {
	Epoch string `json:"epoch"`
}

// for unmarshalling CL syncing data
type clSyncingStruct struct {
	Data clSyncingData `json:"data"`
}

type clSyncingData struct {
	IsSyncing bool `json:"is_syncing"`
}

// for unmarshalling EL syncing data
type elSyncingDataResponse struct {
	Result bool `json:"result"`
}
