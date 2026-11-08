// Package perm 提供 Telegram 会话平台权限的名称。
//
// 插件以 sdk.PermissionName(perm.ChatAdmin) 等方式引用这些常量；
// 本包不链接 Adapter 运行时代码。权限与隐含关系见 CONTRACTS §8。
package perm

// ChatOwner 标识 Telegram 会话所有者的权限。
const ChatOwner = "telegram.chat_owner"

// ChatAdmin 标识 Telegram 会话管理员的权限。
const ChatAdmin = "telegram.chat_admin"
