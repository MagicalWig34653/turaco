#!/usr/bin/env bash
# LAB ONLY: creates the read-only service account, departments, users and nested groups.
set -euo pipefail
PW="${LAB_USER_PASSWORD:?}"
SVC_PW="${LAB_SERVICE_PASSWORD:?}"
st() { samba-tool "$@" >/dev/null; }

# Service account for directory sync (default Authenticated Users read access only).
st user create svc-turaco "$SVC_PW" --description="Turaco lab read-only sync account"
st user setexpiry svc-turaco --noexpiry

st ou create "OU=Staff"
for ou in IT Finance Sales HR; do st ou create "OU=$ou,OU=Staff"; done

# sam;given;surname;department;title
users="
alice.admin;Alice;Admin;IT;IT Administrator
bob.helpdesk;Bob;Helpdesk;IT;Helpdesk Technician
carla.support;Carla;Support;IT;Helpdesk Technician
dieter.finance;Dieter;Finanz;Finance;Accountant
eva.finance;Eva;Buchholz;Finance;Controller
frank.sales;Frank;Vertrieb;Sales;Sales Manager
gina.sales;Gina;Verkauf;Sales;Account Executive
hans.hr;Hans;Personal;HR;HR Manager
ines.hr;Ines;Recruiting;HR;Recruiter
jonas.sales;Jonas;Ehemalig;Sales;Sales Representative
kira.it;Kira;Netzwerk;IT;Network Engineer
leo.finance;Leo;Rechnung;Finance;Accounts Payable
"
while IFS=';' read -r sam given sur dept title; do
  [ -z "$sam" ] && continue
  st user create "$sam" "$PW" --userou="OU=$dept,OU=Staff" --given-name="$given" --surname="$sur" \
    --mail-address="$sam@lab.turaco.test" --department="$dept" --job-title="$title" --company="Turaco Lab GmbH"
  st user setexpiry "$sam" --noexpiry
done <<<"$users"

# Disabled account (for the "disabled user" sync case).
st user disable jonas.sales

# Groups: GRP-IT-Helpdesk is nested in GRP-IT; department groups are nested in GRP-All-Staff.
for g in GRP-IT GRP-IT-Helpdesk GRP-Finance GRP-Sales GRP-HR GRP-All-Staff; do st group add "$g"; done
st group addmembers GRP-IT alice.admin,kira.it,GRP-IT-Helpdesk
st group addmembers GRP-IT-Helpdesk bob.helpdesk,carla.support
st group addmembers GRP-Finance dieter.finance,eva.finance,leo.finance
st group addmembers GRP-Sales frank.sales,gina.sales,jonas.sales
st group addmembers GRP-HR hans.hr,ines.hr
st group addmembers GRP-All-Staff GRP-IT,GRP-Finance,GRP-Sales,GRP-HR
echo "lab users provisioned"
