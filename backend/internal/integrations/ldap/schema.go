package ldap

import (
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// schema describes where one directory type keeps the attributes Turaco reads.
// The attribute lists are the complete allowlist: nothing else is ever
// requested, in particular not "*", userPassword, unicodePwd or LAPS
// attributes.
type schema struct {
	// idAttr holds the stable object identifier. For Active Directory it is
	// the binary objectGUID, for OpenLDAP the textual entryUUID.
	idAttr         string
	idIsBinaryGUID bool
	usernameAttr   string
	// employeeAttrs are tried in order.
	employeeAttrs []string
	// hasAccountControl is true when the enabled state comes from
	// userAccountControl; otherwise every account is enabled.
	hasAccountControl bool

	userAttrs  []string
	groupAttrs []string
}

const (
	attrCN            = "cn"
	attrDisplayName   = "displayName"
	attrGivenName     = "givenName"
	attrSurname       = "sn"
	attrMail          = "mail"
	attrManager       = "manager"
	attrDescription   = "description"
	attrMember        = "member"
	attrAccountCtl    = "userAccountControl"
	attrEmployeeID    = "employeeID"
	attrEmployeeNum   = "employeeNumber"
	attrObjectGUID    = "objectGUID"
	attrEntryUUID     = "entryUUID"
	attrSAMAccount    = "sAMAccountName"
	attrUID           = "uid"
	uacAccountDisable = 0x2
)

func schemaFor(directoryType string) (schema, error) {
	switch directoryType {
	case config.DirectoryTypeActiveDirectory:
		return schema{
			idAttr:            attrObjectGUID,
			idIsBinaryGUID:    true,
			usernameAttr:      attrSAMAccount,
			employeeAttrs:     []string{attrEmployeeID, attrEmployeeNum},
			hasAccountControl: true,
			userAttrs: []string{
				attrObjectGUID, attrSAMAccount, attrDisplayName, attrCN, attrGivenName, attrSurname,
				attrMail, attrEmployeeID, attrEmployeeNum, attrManager, attrAccountCtl,
			},
			groupAttrs: []string{attrObjectGUID, attrCN, attrDisplayName, attrDescription, attrMember},
		}, nil
	case config.DirectoryTypeOpenLDAP:
		return schema{
			idAttr:        attrEntryUUID,
			usernameAttr:  attrUID,
			employeeAttrs: []string{attrEmployeeNum},
			userAttrs: []string{
				attrEntryUUID, attrUID, attrDisplayName, attrCN, attrGivenName, attrSurname,
				attrMail, attrEmployeeNum, attrManager,
			},
			groupAttrs: []string{attrEntryUUID, attrCN, attrDisplayName, attrDescription, attrMember},
		}, nil
	default:
		return schema{}, fmt.Errorf("ldap: unsupported directory type")
	}
}
