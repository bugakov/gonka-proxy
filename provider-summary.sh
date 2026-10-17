#!/usr/bin/env bash
# provider-summary.sh — single-command health summary for gonka-proxy.
#
# Reads GET /health from the running proxy and prints a compact per-provider
# table (status, success/error counts, last success/error, cooldown) with
# status coloring and active-cooldown highlighting, plus a footer that shows
# which provider answered the last request.
#
# Usage:  ./provider-summary.sh
# Env:    GONKA_HEALTH_URL   override the proxy health URL
#         NO_COLOR           set to disable ANSI colors
# Exit:   0 on success, 1 if the proxy health endpoint is unreachable.

set -euo pipefail

HEALTH_URL=${GONKA_HEALTH_URL:-"http://127.0.0.1:58081/health"}

# Colors are enabled only on a TTY unless NO_COLOR is set.
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    C_RESET=$'\033[0m'
    C_BOLD=$'\033[1m'
    C_DIM=$'\033[2m'
    C_GREEN=$'\033[32m'
    C_YELLOW=$'\033[33m'
    C_RED=$'\033[31m'
else
    C_RESET=""; C_BOLD=""; C_DIM=""; C_GREEN=""; C_YELLOW=""; C_RED=""
fi

if ! health=$(curl -sS --max-time 10 "$HEALTH_URL"); then
    echo "error: cannot reach gonka-proxy at $HEALTH_URL" >&2
    echo "is the service running? check: oxmgr status gonka-proxy" >&2
    exit 1
fi

today=$(date -u +%F)

# Short timestamp: today -> HH:MM:SS, otherwise -> MM-DD HH:MM.
short_ts() {
    local ts=$1 d t
    d=${ts:0:10}
    t=${ts:11:8}
    if [[ $d == "$today" ]]; then
        printf '%s' "$t"
    else
        printf '%s %s' "${d:5}" "${t:0:5}"
    fi
}

declare -a order=()
declare -A p_status p_succ p_err p_last_succ p_last_err p_cat p_cat_status p_cooldown

provider=""
while IFS= read -r line; do
    case "$line" in
        provider\ *)
            provider=${line#provider \"}
            provider=${provider%%\"*}
            p_status[$provider]=${line##*status=}
            order+=("$provider")
            ;;
        *successes=*)
            p_succ[$provider]=${line#*successes=}; p_succ[$provider]=${p_succ[$provider]%% *}
            p_err[$provider]=${line#*errors=};      p_err[$provider]=${p_err[$provider]%% *}
            ;;
        *last_success=*)
            p_last_succ[$provider]=${line#*last_success=}
            ;;
        *last_error=*)
            p_last_err[$provider]=${line#*last_error=}
            p_last_err[$provider]=${p_last_err[$provider]%% *}
            p_cat[$provider]=${line#*category=}; p_cat[$provider]=${p_cat[$provider]%% *}
            p_cat_status[$provider]=${line##*status=}
            ;;
        *cooldown=*)
            cd=${line#*cooldown=}
            cd=${cd%% *}
            if [[ $cd == until=* ]]; then
                p_cooldown[$provider]=${cd#until=}
            else
                p_cooldown[$provider]=""
            fi
            ;;
    esac
done <<< "$health"

# Summary counters and the provider that answered last (most recent success).
h=0; d=0; u=0
last_provider=""; last_ts=""
for pr in "${order[@]}"; do
    case "${p_status[$pr]}" in
        healthy)     ((h += 1)) ;;
        degraded)    ((d += 1)) ;;
        unavailable) ((u += 1)) ;;
    esac
    if [[ -n "${p_last_succ[$pr]:-}" && ( -z "$last_ts" || "${p_last_succ[$pr]}" > "$last_ts" ) ]]; then
        last_provider=$pr
        last_ts=${p_last_succ[$pr]}
    fi
done

color_status() {
    case "$1" in
        healthy)     printf '%s%s%s' "$C_GREEN" "$1" "$C_RESET" ;;
        degraded)    printf '%s%s%s' "$C_YELLOW" "$1" "$C_RESET" ;;
        unavailable) printf '%s%s%s' "$C_RED" "$1" "$C_RESET" ;;
        *)           printf '%s' "$1" ;;
    esac
}

sep="${C_DIM}--------------------------------------------------------------------------------------------${C_RESET}"

printf '%b' "${C_BOLD}"
printf '%-18s %-12s %7s %6s  %-16s %-24s %s' "PROVIDER" "STATUS" "SUCC" "ERR" "LAST SUCCESS" "LAST ERROR" "COOLDOWN"
printf '%b\n' "${C_RESET}"
printf '%s\n' "$sep"

for pr in "${order[@]}"; do
    status=${p_status[$pr]}
    succ=${p_succ[$pr]:-0}
    err=${p_err[$pr]:-0}
    ls=$(short_ts "${p_last_succ[$pr]:-}")

    le=${p_last_err[$pr]:-}
    le_stat=${p_cat_status[$pr]:-0}
    if [[ -n $le ]]; then
        if [[ -n "${p_cat[$pr]:-}" ]]; then
            if [[ $le_stat != "0" && -n $le_stat ]]; then
                last_err_disp="${p_cat[$pr]} (HTTP $le_stat)"
            else
                last_err_disp="${p_cat[$pr]}"
            fi
        else
            last_err_disp=$le
        fi
    else
        last_err_disp="never"
    fi

    cd=${p_cooldown[$pr]:-}
    if [[ -n $cd ]]; then
        cd_disp="${C_RED}until $(short_ts "$cd")${C_RESET}"
    else
        cd_disp="${C_DIM}--${C_RESET}"
    fi

    printf '%-18s %s %7s %6s  %-16s %-24s %s\n' \
        "$pr" "$(color_status "$status")" "$succ" "$err" "$ls" "$last_err_disp" "$cd_disp"
done

printf '%s\n' "$sep"
printf '%s%s healthy%s, %s%s degraded%s, %s%s unavailable%s\n' \
    "$C_GREEN" "$h" "$C_RESET" "$C_YELLOW" "$d" "$C_RESET" "$C_RED" "$u" "$C_RESET"
if [[ -n $last_provider ]]; then
    printf 'last answered: %s%s%s at %s\n' "$C_BOLD" "$last_provider" "$C_RESET" "$(short_ts "$last_ts")"
fi