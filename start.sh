#!/usr/bin/env sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
mode=""
build="false"
image=""
demo_image=""
ssh_image=""
instance="bid-mon"
port="18080"
while [ "$#" -gt 0 ]; do
    case "$1" in
        --mode|--image|--demo-image|--ssh-image|--instance|--port)
            option="$1"; shift
            [ "$#" -gt 0 ] || { printf '%s\n' 'Missing option value.' >&2; exit 1; }
            case "$option" in
                --mode) mode="$1" ;; --image) image="$1" ;;
                --demo-image) demo_image="$1" ;; --ssh-image) ssh_image="$1" ;;
                --instance) instance="$1" ;; --port) port="$1" ;;
            esac ;;
        --build) build="true" ;;
        *) printf '%s\n' 'Usage: sh start.sh [--mode business|demo] [--build] [--image IMAGE] [--demo-image IMAGE] [--ssh-image IMAGE] [--instance NAME] [--port PORT]' >&2; exit 1 ;;
    esac
    shift
done
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'Install Docker and Docker Compose first.' >&2; exit 1; }
docker info --format '{{.ServerVersion}}'
docker compose version --short
read_value() {
    # Read values without executing .env as shell code or printing secret lines.
    if [ -f .env ]; then
        value=$(awk -v key="$1" 'index($0,key "=")==1 {print substr($0,length(key)+2); exit}' .env)
        if [ -n "$value" ]; then printf '%s' "$value"; return; fi
    fi
    printf '%s' "$2"
}
[ -n "$mode" ] || mode=$(read_value BID_MON_MODE business)
[ -n "$image" ] || image=$(read_value BID_MON_IMAGE bid-mon:2.0.0-dev)
[ -n "$demo_image" ] || demo_image=$(read_value BID_MON_LAB_IMAGE bid-mon-lab:2.0.0-dev)
[ -n "$ssh_image" ] || ssh_image=$(read_value BID_MON_SSH_IMAGE bid-mon-go-ssh:2.0.0-dev)
case "$mode" in business|demo) ;; *) printf '%s\n' 'Mode must be business or demo.' >&2; exit 1 ;; esac
export BID_MON_IMAGE="$image"
export BID_MON_LAB_IMAGE="$demo_image"
export BID_MON_SSH_IMAGE="$ssh_image"
if [ "$build" = true ]; then
    docker build -f Dockerfile.go --target runtime -t "$image" .
elif [ "$image" = bid-mon:2.0.0-dev ] && ! docker image inspect "$image" >/dev/null 2>&1; then
    printf '%s\n' 'The development image has not been published. Use sh start.sh --build or supply --image with an available fixed release image.' >&2
    exit 1
fi
owner_uid=$(id -u)
owner_gid=$(id -g)
# Root installations use the default non-root runtime account.
if [ "$owner_uid" = 0 ]; then owner_uid=10001; owner_gid=10001; fi
docker compose -p "$instance-bootstrap" -f compose.bootstrap.yaml run --rm -T initializer init --dir /workspace --mode "$mode" --image "$image" --demo-image "$demo_image" --ssh-image "$ssh_image" --instance "$instance" --port "$port" --uid "$owner_uid" --gid "$owner_gid"
set -- compose --env-file .env -f compose.yaml
if [ "$mode" = demo ]; then
    set -- "$@" -f compose.demo.yaml
    if [ "$build" = true ]; then
        docker build -f Dockerfile -t "$demo_image" .
        docker build -f Dockerfile.go --target ssh-target -t "$ssh_image" .
    fi
fi
docker "$@" up -d --wait --wait-timeout 180
printf 'Application: http://127.0.0.1:%s\n' "$(read_value BID_MON_HTTP_PORT "$port")"
printf '%s\n' 'Read your administrator token from deploy/phase2/config/credentials.json. Existing credentials were preserved.'
