#!/bin/sh

# OpenLDAP setup script for OpenWRT
# Run this script on your OpenWRT router (192.168.1.1)

set -e

echo "=== OpenLDAP Setup for OpenWRT ==="
echo "Router: 192.168.1.1"
echo "Domain: amyinfo.com"
echo "Base DN: dc=amyinfo,dc=com"
echo ""

# Check if openldap is installed
if ! opkg list-installed | grep -q openldap-server; then
    echo "Installing openldap-server and openldap-utils..."
    opkg update
    opkg install openldap-server openldap-utils
fi

# Generate password hashes (run these commands on the router)
echo "=== Generate password hashes ==="
echo "Run these commands on the router to generate password hashes:"
echo "  slappasswd -s 'your_admin_password'  # for admin"
echo "  slappasswd -s 'kyra_password'     # for kyra"
echo "  slappasswd -s 'alice_password'    # for alice"
echo "  slappasswd -s 'bob_password'      # for bob"
echo "  slappasswd -s 'charlie_password'  # for charlie"
echo ""
echo "Then replace {SSHA}CHANGE_XXX in the .ldif files with the actual hashes"
echo ""

# Copy configuration to router
echo "=== Copy config files to router ==="
echo "Run these commands on your local machine:"
echo "  scp openldap/slapd.conf root@192.168.1.1:/etc/openldap/slapd.conf"
echo "  scp openldap/start.ldif root@192.168.1.1:/tmp/start.ldif"
echo "  scp openldap/users.ldif root@192.168.1.1:/tmp/users.ldif"
echo ""

# Commands to run on the router
echo "=== Commands to run on the router ==="
echo "1. Stop slapd if running:"
echo "   /etc/init.d/slapd stop"
echo ""
echo "2. Backup original config (optional):"
echo "   cp /etc/openldap/slapd.conf /etc/openldap/slapd.conf.bak"
echo ""
echo "3. Copy new config:"
echo "   cp /tmp/slapd.conf /etc/openldap/slapd.conf"
echo ""
echo "4. Initialize the database directory:"
echo "   mkdir -p /var/lib/openldap"
echo "   chown ldap:ldap /var/lib/openldap"
echo ""
echo "5. Add base entries:"
echo "   ldapadd -x -H ldap:/// -D 'cn=admin,dc=amyinfo,dc=com' -W -f /tmp/start.ldif"
echo ""
echo "6. Add users:"
echo "   ldapadd -x -H ldap:/// -D 'cn=admin,dc=amyinfo,dc=com' -W -f /tmp/users.ldif"
echo ""
echo "7. Start slapd:"
echo "   /etc/init.d/slapd start"
echo "   /etc/init.d/slapd enable  # start on boot"
echo ""
echo "8. Test the setup:"
echo "   ldapsearch -x -W -D 'cn=admin,dc=amyinfo,dc=com' -H ldap:/// -b 'dc=amyinfo,dc=com'"
echo "   ldapsearch -x -W -D 'cn=admin,dc=amyinfo,dc=com' -H ldap:/// -b 'ou=groups,dc=amyinfo,dc=com' '(objectClass=posixGroup)'"
echo ""

# Local test (if you have ldap-utils installed)
echo "=== After setup, test from your local machine ==="
echo "ldapsearch -x -H ldap://192.168.1.1 -D 'cn=admin,dc=amyinfo,dc=com' -W -b 'dc=amyinfo,dc=com'"
echo ""
echo "Test user authentication:"
echo "ldapwhoami -x -H ldap://192.168.1.1 -D 'uid=kyra,ou=people,dc=amyinfo,dc=com' -W"
echo ""
echo "Check group membership:"
echo "ldapsearch -x -H ldap://192.168.1.1 -D 'cn=admin,dc=amyinfo,dc=com' -W -b 'ou=groups,dc=amyinfo,dc=com' '(&(objectClass=posixGroup)(memberUid=kyra))'"
