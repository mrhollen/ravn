package domain

// ProjectMember is one stored membership row: identityID holds Role in
// projectID. It travels from the identity repository's write side (multi-user
// T9) to the Memberships use case and nowhere else — visibility/authorization
// policies keep reasoning about Subject + EffectiveRole, never about rows.
type ProjectMember struct {
	ProjectID  ID
	IdentityID ID
	Role       Role // always Storable: viewer/editor/admin; Owner is derived from created_by, never stored
}
