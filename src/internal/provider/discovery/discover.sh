#!/bin/sh
# Strange Company node discovery, script version 1 (plan 05 section 14.2).
#
# Read by `sh -s` on the node, as the non-root login user. It prints ONE JSON document on stdout and changes nothing:
# no file is created or written (no redirection to a file, no here-document, no temporary file), no command runs
# as root and no package or service is touched. Every fact comes from a read-only command; a required command that the
# node lacks is listed in "missing_tools" and its fact is left out. The document format is described in
# test/fixtures/existing/discovery/README.md. Never use bashisms here (sh is dash on Debian and Ubuntu).

LC_ALL=C
export LC_ALL
PATH="$PATH:/usr/sbin:/sbin:/usr/local/sbin"
export PATH

have() { command -v "$1" >/dev/null 2>&1; }

# json_str prints its standard input as one JSON string (backslash, quote and control characters escaped).
json_str() {
	printf '"'
	od -An -v -tx1 | tr -d ' \n' | sed 's/\(..\)/\1 /g' | awk '
		BEGIN { for (i = 0; i < 256; i++) hex[sprintf("%02x", i)] = i }
		{
			for (i = 1; i <= NF; i++) {
				n = hex[$i]
				if (n == 34) printf "\\\""
				else if (n == 92) printf "\\\\"
				else if (n == 10) printf "\\n"
				else if (n == 13) printf "\\r"
				else if (n == 9) printf "\\t"
				else if (n < 32) printf "\\u%04x", n
				else printf "%c", n
			}
		}'
	printf '"'
}

# str prints the value of the arguments joined by spaces as a JSON string.
str() { printf '%s' "$*" | json_str; }

# txt_of prints the standard output of a command as a JSON string (empty when the command fails).
txt_of() { "$@" 2>/dev/null | json_str; }

# json_of prints the standard output of a command that prints JSON, or the fallback when it fails or prints nothing.
json_of() {
	fallback="$1"
	shift
	out=$("$@" 2>/dev/null)
	if [ -n "$out" ]; then printf '%s' "$out"; else printf '%s' "$fallback"; fi
}

missing=""
need() {
	if ! have "$1"; then
		if [ -n "$missing" ]; then missing="$missing,"; fi
		missing="$missing\"$1\""
		return 1
	fi
	return 0
}

for t in sh nproc lsblk findmnt ip ss uname stat timedatectl; do need "$t" || :; done

# first_file prints the content of the first readable file, else nothing.
first_file() {
	for f in "$@"; do
		if [ -r "$f" ]; then cat "$f" 2>/dev/null; return 0; fi
	done
	return 0
}

nproc_v=0
if have nproc; then nproc_v=$(nproc --all 2>/dev/null || echo 0); fi
cpu_model=$(sed -n 's/^model name[[:space:]]*:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -n 1)

if have lsblk; then
	lsblk_j=$(json_of 'null' lsblk -J -b -o NAME,KNAME,PKNAME,SIZE,ROTA,TYPE,TRAN,MODEL,MOUNTPOINTS)
else
	lsblk_j=null
fi

if have findmnt; then
	findmnt_j=null
	for p in /var/lib/rancher /var/lib /; do
		out=$(findmnt -J -b -T "$p" 2>/dev/null)
		if [ -n "$out" ]; then findmnt_j="$out"; break; fi
	done
else
	findmnt_j=null
fi

kernel=""
arch=""
if have uname; then
	kernel=$(uname -r 2>/dev/null)
	arch=$(uname -m 2>/dev/null)
fi

virt=unknown
if have systemd-detect-virt; then
	virt=$(systemd-detect-virt 2>/dev/null || true)
	if [ -z "$virt" ]; then virt=none; fi
fi

cgroup_fs=""
if have stat; then cgroup_fs=$(stat -fc %T /sys/fs/cgroup 2>/dev/null); fi

apparmor=""
if [ -r /sys/module/apparmor/parameters/enabled ]; then apparmor=$(cat /sys/module/apparmor/parameters/enabled 2>/dev/null); fi

wireguard=false
if [ -d /sys/module/wireguard ]; then
	wireguard=true
elif have modinfo && modinfo wireguard >/dev/null 2>&1; then
	wireguard=true
fi

ntp=""
if have timedatectl; then ntp=$(timedatectl show -p NTPSynchronized --value 2>/dev/null); fi

swap=""
if have swapon; then swap=$(swapon --show=SIZE --bytes --noheadings 2>/dev/null); fi

if have ip; then
	ip_addr_j=$(json_of '[]' ip -j addr)
	ip_route_j=$(json_of '[]' ip -j route get 1.1.1.1)
else
	ip_addr_j='[]'
	ip_route_j='[]'
fi

ss_t=""
if have ss; then ss_t=$(ss -Hltnu 2>/dev/null); fi

# platforms: names found of k3s, kubelet, dockerd and containerd (binary, unit or running process).
platforms=""
add_platform() {
	if [ -n "$platforms" ]; then platforms="$platforms,"; fi
	platforms="$platforms\"$1\""
}
procs=$(for c in /proc/[0-9]*/comm; do cat "$c" 2>/dev/null; done)
has_proc() { printf '%s\n' "$procs" | grep -qx "$1"; }
for p in k3s kubelet dockerd containerd; do
	found=no
	if have "$p"; then found=yes; fi
	for d in /usr/local/bin /usr/bin /usr/sbin /usr/local/sbin; do
		if [ -e "$d/$p" ]; then found=yes; fi
	done
	for d in /etc/systemd/system /lib/systemd/system /usr/lib/systemd/system; do
		if [ -e "$d/$p.service" ]; then found=yes; fi
	done
	if has_proc "$p"; then found=yes; fi
	if [ "$p" = k3s ] && [ -e /etc/rancher ]; then found=yes; fi
	if [ $found = yes ]; then add_platform "$p"; fi
done

install_uid=""
if [ -r /etc/strange-company/install-uid ]; then install_uid=$(cat /etc/strange-company/install-uid 2>/dev/null); fi

sudo_ok=false
if have sudo && sudo -n true >/dev/null 2>&1; then sudo_ok=true; fi

machine_id=""
if [ -r /etc/machine-id ]; then machine_id=$(cat /etc/machine-id 2>/dev/null); fi

printf '{'
printf '"script_version":1,'
printf '"nproc":%s,' "${nproc_v:-0}"
printf '"cpu_model":%s,' "$(str "$cpu_model")"
printf '"meminfo":%s,' "$(txt_of cat /proc/meminfo)"
printf '"lsblk":%s,' "$lsblk_j"
printf '"findmnt":%s,' "$findmnt_j"
printf '"os_release":%s,' "$(txt_of cat /etc/os-release)"
printf '"kernel":%s,' "$(str "$kernel")"
printf '"arch":%s,' "$(str "$arch")"
printf '"virt":%s,' "$(str "$virt")"
printf '"cgroup_fs":%s,' "$(str "$cgroup_fs")"
printf '"apparmor":%s,' "$(str "$apparmor")"
printf '"wireguard":%s,' "$wireguard"
printf '"ntp_synchronized":%s,' "$(str "$ntp")"
printf '"swap":%s,' "$(str "$swap")"
printf '"ip_addr":%s,' "$ip_addr_j"
printf '"ip_route_get":%s,' "$ip_route_j"
printf '"ss":%s,' "$(str "$ss_t")"
printf '"platforms":[%s],' "$platforms"
printf '"install_uid":%s,' "$(str "$install_uid")"
printf '"sudo_ok":%s,' "$sudo_ok"
printf '"machine_id":%s,' "$(str "$machine_id")"
printf '"missing_tools":[%s]' "$missing"
printf '}\n'
