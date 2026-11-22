package action

import "encoding/json"

// UnmarshalJSON 将 Bot API 的整数 ID 精确读取为十进制字符串。
func (c *Chat) UnmarshalJSON(raw []byte) error {
	type fields Chat
	decoded := struct {
		*fields
		ID json.Number `json:"id"`
	}{fields: (*fields)(c)}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	c.ID = decoded.ID.String()
	return nil
}

// UnmarshalJSON 将 Bot API 的整数用户 ID 精确读取为十进制字符串。
func (u *User) UnmarshalJSON(raw []byte) error {
	type fields User
	decoded := struct {
		*fields
		ID json.Number `json:"id"`
	}{fields: (*fields)(u)}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	u.ID = decoded.ID.String()
	return nil
}
