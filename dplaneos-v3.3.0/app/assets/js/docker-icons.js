/* Docker Container Icons - D-PlaneOS
   100% offline. Uses local Material Symbols, zero CDN dependencies. */

const CONTAINER_ICONS = {
  'plex':       'live_tv',
  'jellyfin':   'live_tv',
  'emby':       'live_tv',
  'nextcloud':  'cloud',
  'sonarr':     'tv_guide',
  'radarr':     'movie',
  'lidarr':     'music_note',
  'portainer':  'dashboard',
  'grafana':    'monitoring',
  'prometheus': 'query_stats',
  'traefik':    'router',
  'nginx':      'dns',
  'caddy':      'dns',
  'redis':      'memory',
  'postgres':   'database',
  'mariadb':    'database',
  'mongodb':    'database',
  'mysql':      'database',
  'pihole':     'shield',
  'adguard':    'shield',
  'homeassistant': 'home',
  'vaultwarden':'lock',
  'bitwarden':  'lock',
  'wireguard':  'vpn_key',
  'syncthing':  'sync',
  'duplicati':  'backup',
  'restic':     'backup',
  'uptime-kuma':'monitor_heart',
  'watchtower': 'update',
  'homepage':   'web',
  'homarr':     'web',
  'code-server':'code',
  'gitea':      'code',
  'transmission':'download',
  'qbittorrent':'download',
  'filebrowser':'folder_open'
};

function detectIconName(imageName) {
  if (!imageName) return 'deployed_code';
  var name = imageName.toLowerCase().split('/').pop().split(':')[0];
  for (var key in CONTAINER_ICONS) {
    if (name.indexOf(key) !== -1) return CONTAINER_ICONS[key];
  }
  return 'deployed_code';
}

function getContainerIcon(container) {
  return detectIconName(container.image);
}

function renderContainerIcon(container, size) {
  size = size || 48;
  var icon = detectIconName(container.image);
  return '<span class="material-symbols-rounded" style="font-size:' + size + 'px">' + icon + '</span>';
}
