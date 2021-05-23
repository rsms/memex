package service

type PutCommand struct {
	// required
	Type string `json:"type"` // e.g. "twitter.status"
	File string `json:"file"` // either File or Data must be provided
	Data []byte `json:"data"`

	// optional
	Id   string   `json:"id"`   // if set: replace if exists
	Name string   `json:"name"` //
	Tags []string `json:"tags"` // text to be tokenized for indexing
}

type TypeDef struct {
	Id       string `json:"id"`
	ParentId string `json:"parent_id"`
	Name     string `json:"name"`
}

type TypeRegCommand struct {
	Types []TypeDef
}
