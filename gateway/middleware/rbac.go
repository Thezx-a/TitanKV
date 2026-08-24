package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Permission 命名规范: resource:action (如 kv:put, collection:create, rag:ingest).
type Permission string

const (
	PermKVGet            Permission = "kv:get"
	PermKVPut            Permission = "kv:put"
	PermKVDelete         Permission = "kv:delete"
	PermCollectionCreate Permission = "collection:create"
	PermCollectionDelete Permission = "collection:delete"
	PermUserManage       Permission = "user:manage"
	PermAPIKeyIssue      Permission = "apikey:issue"
	PermRAGIngest        Permission = "rag:ingest" // 文档入库 / 删除 / 任务查询
	PermRAGQuery         Permission = "rag:query"  // 检索 / 流式问答 / 文档列表查看
)

// rolePerms 简化示例; 生产从 DB / etcd 加载.
var rolePerms = map[string]map[Permission]bool{
	"admin": {
		PermKVGet: true, PermKVPut: true, PermKVDelete: true,
		PermCollectionCreate: true, PermCollectionDelete: true,
		PermUserManage: true, PermAPIKeyIssue: true,
		PermRAGIngest: true, PermRAGQuery: true,
	},
	"writer": {
		PermKVGet: true, PermKVPut: true, PermCollectionCreate: true,
		PermRAGIngest: true, PermRAGQuery: true,
	},
	"reader": {
		PermKVGet:    true,
		PermRAGQuery: true,
	},
}

// RoleHasPermission 暴露给外部使用 (如 handler 内细粒度判断).
func RoleHasPermission(role string, perm Permission) bool {
	if m, ok := rolePerms[role]; ok {
		return m[perm]
	}
	return false
}

// RBAC 校验当前用户角色是否拥有 requiredPerm 权限. 失败返回 403.
// 在 Auth 之后注册: 先识别身份, 再判断权限.
func RBAC(requiredPerm Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleVal, exists := c.Get("role")
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":      "no role in context",
				"request_id": GetRequestID(c),
			})
			return
		}
		role, _ := roleVal.(string)
		if !RoleHasPermission(role, requiredPerm) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":      "permission denied",
				"required":   string(requiredPerm),
				"role":       role,
				"request_id": GetRequestID(c),
			})
			return
		}
		c.Next()
	}
}
