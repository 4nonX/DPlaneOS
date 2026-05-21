//go:build linux && cgo && libzfs

package libzfs

// Direct libzfs bindings via cgo.
// Active only when: target OS is linux AND CGO_ENABLED=1.
// Built by mkDaemonCGO in flake.nix which adds pkgs.zfs to buildInputs.
//
// Thread safety: libzfs_init() returns a handle that must not be shared
// across OS threads. withHandle() acquires runtime.LockOSThread() for the
// duration of each call, giving each goroutine exclusive OS-thread ownership
// of its handle. This matches the TrueNAS/OpenZFS recommended usage pattern.

/*
#cgo LDFLAGS: -lzfs -lnvpair -lzfs_core -luutil -lm
#cgo CFLAGS: -D_GNU_SOURCE

#include <libzfs.h>
#include <sys/nvpair.h>
#include <string.h>
#include <stdlib.h>

// ── vdev-tree traversal ────────────────────────────────────────────────────

// find_device_in_vdev recursively walks an nvlist vdev tree and returns 1
// if the given device path appears anywhere in the tree.
static int find_device_in_vdev(nvlist_t *nv, const char *device) {
    char *path = NULL;
    if (nvlist_lookup_string(nv, ZPOOL_CONFIG_PATH, &path) == 0 && path != NULL) {
        if (strcmp(path, device) == 0) {
            return 1;
        }
    }
    nvlist_t **children = NULL;
    uint_t nchildren = 0;
    if (nvlist_lookup_nvlist_array(nv, ZPOOL_CONFIG_CHILDREN, &children, &nchildren) == 0) {
        for (uint_t i = 0; i < nchildren; i++) {
            if (find_device_in_vdev(children[i], device)) {
                return 1;
            }
        }
    }
    return 0;
}

// ── pool membership context and callback ───────────────────────────────────

typedef struct {
    const char *device;
    int         found;
    char        pool_name[256];
} dplane_find_ctx_t;

// pool_find_device_cb is the zpool_iter callback. Inspects each pool's vdev
// tree for the target device. Returns non-zero to stop iteration on match.
static int pool_find_device_cb(zpool_handle_t *zhp, void *data) {
    dplane_find_ctx_t *ctx = (dplane_find_ctx_t *)data;
    nvlist_t *config = zpool_get_config(zhp, NULL);
    if (config != NULL) {
        nvlist_t *nvroot = NULL;
        if (nvlist_lookup_nvlist(config, ZPOOL_CONFIG_VDEV_TREE, &nvroot) == 0) {
            if (find_device_in_vdev(nvroot, ctx->device)) {
                ctx->found = 1;
                strncpy(ctx->pool_name, zpool_get_name(zhp), sizeof(ctx->pool_name) - 1);
                ctx->pool_name[sizeof(ctx->pool_name) - 1] = '\0';
            }
        }
    }
    zpool_close(zhp);
    return ctx->found;
}

// ── C helper functions ─────────────────────────────────────────────────────
// Each helper opens/closes its own pool or dataset handle so Go does not
// need to manage C handle lifetimes beyond the libzfs_handle_t.

// dplane_pool_is_member: returns 1 if device belongs to any active pool.
// Sets pool_name_out (up to pool_name_len bytes) to the owning pool name.
static int dplane_pool_is_member(libzfs_handle_t *hdl,
                                 const char *device,
                                 char *pool_name_out,
                                 size_t pool_name_len) {
    dplane_find_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.device = device;
    zpool_iter(hdl, pool_find_device_cb, &ctx);
    if (ctx.found && pool_name_out != NULL && pool_name_len > 0) {
        strncpy(pool_name_out, ctx.pool_name, pool_name_len - 1);
        pool_name_out[pool_name_len - 1] = '\0';
    }
    return ctx.found;
}

// import_pool_iter_cb imports each pool found by dplane_pool_import_all.
static int import_pool_iter_cb(nvpair_t *pair, libzfs_handle_t *hdl) {
    nvlist_t *config;
    if (nvpair_value_nvlist(pair, &config) != 0) return 0;
    return zpool_import(hdl, config, NULL, NULL);
}

// dplane_pool_import_all searches search_path for importable pools and
// imports all of them with force (-f). Returns number of import errors.
static int dplane_pool_import_all(libzfs_handle_t *hdl, const char *search_path) {
    char *path = (char *)search_path;
    importargs_t idata;
    memset(&idata, 0, sizeof(idata));
    idata.path  = &path;
    idata.paths = 1;
    idata.can_be_active = 0;

    nvlist_t *pools = zpool_search_import(hdl, &idata, NULL);
    if (pools == NULL) return 0;

    nvpair_t *elem = NULL;
    int errors = 0;
    while ((elem = nvlist_next_nvpair(pools, elem)) != NULL) {
        if (import_pool_iter_cb(elem, hdl) != 0) {
            errors++;
        }
    }
    nvlist_free(pools);
    return errors;
}

// dplane_pool_export exports the named pool, optionally with force.
static int dplane_pool_export(libzfs_handle_t *hdl, const char *name, int force) {
    zpool_handle_t *zhp = zpool_open(hdl, name);
    if (zhp == NULL) return -1;
    int rc = zpool_export(zhp, force ? B_TRUE : B_FALSE, NULL);
    zpool_close(zhp);
    return rc;
}

// dplane_dataset_create creates a ZFS filesystem.
static int dplane_dataset_create(libzfs_handle_t *hdl, const char *name) {
    return zfs_create(hdl, name, ZFS_TYPE_FILESYSTEM, NULL);
}

// dplane_dataset_get_prop retrieves a property value into buf.
// Supports both native ZFS properties (via zfs_prop_get) and user properties.
static int dplane_dataset_get_prop(libzfs_handle_t *hdl,
                                   const char *dataset,
                                   const char *propname,
                                   char *buf,
                                   size_t buflen) {
    zfs_handle_t *zhp = zfs_open(hdl, dataset, ZFS_TYPE_DATASET);
    if (zhp == NULL) return -1;

    int rc = -1;
    zfs_prop_t prop = zfs_name_to_prop(propname);
    if (prop != ZPROP_INVAL) {
        rc = zfs_prop_get(zhp, prop, buf, buflen, NULL, NULL, 0, B_FALSE);
    } else {
        // User / native string property not in the enum: try user props nvlist.
        nvlist_t *user_props = zfs_get_user_props(zhp);
        if (user_props != NULL) {
            nvlist_t *propval;
            if (nvlist_lookup_nvlist(user_props, propname, &propval) == 0) {
                char *value;
                if (nvlist_lookup_string(propval, ZPROP_VALUE, &value) == 0) {
                    strncpy(buf, value, buflen - 1);
                    buf[buflen - 1] = '\0';
                    rc = 0;
                }
            }
        }
    }
    zfs_close(zhp);
    return rc;
}

// dplane_dataset_set_prop sets a property on a dataset.
static int dplane_dataset_set_prop(libzfs_handle_t *hdl,
                                   const char *dataset,
                                   const char *propname,
                                   const char *propval) {
    zfs_handle_t *zhp = zfs_open(hdl, dataset, ZFS_TYPE_DATASET);
    if (zhp == NULL) return -1;
    int rc = zfs_prop_set(zhp, propname, propval);
    zfs_close(zhp);
    return rc;
}

// dplane_dataset_promote promotes a ZFS clone to a full dataset.
static int dplane_dataset_promote(libzfs_handle_t *hdl, const char *dataset) {
    zfs_handle_t *zhp = zfs_open(hdl, dataset, ZFS_TYPE_DATASET);
    if (zhp == NULL) return -1;
    int rc = zfs_promote(zhp);
    zfs_close(zhp);
    return rc;
}

// dplane_vdev_detach detaches a device from a ZFS mirror.
static int dplane_vdev_detach(libzfs_handle_t *hdl,
                              const char *pool,
                              const char *device) {
    zpool_handle_t *zhp = zpool_open(hdl, pool);
    if (zhp == NULL) return -1;
    int rc = zpool_vdev_detach(zhp, device);
    zpool_close(zhp);
    return rc;
}

// dplane_vdev_online brings a pool device online.
static int dplane_vdev_online(libzfs_handle_t *hdl,
                              const char *pool,
                              const char *device) {
    zpool_handle_t *zhp = zpool_open(hdl, pool);
    if (zhp == NULL) return -1;
    vdev_state_t newstate;
    int rc = zpool_vdev_online(zhp, device, 0, &newstate);
    zpool_close(zhp);
    return rc;
}

// dplane_vdev_offline takes a pool device offline.
// temporary=1 mirrors `zpool offline -t` (device comes back on import).
static int dplane_vdev_offline(libzfs_handle_t *hdl,
                               const char *pool,
                               const char *device,
                               int temporary) {
    zpool_handle_t *zhp = zpool_open(hdl, pool);
    if (zhp == NULL) return -1;
    int rc = zpool_vdev_offline(zhp, device, temporary ? B_TRUE : B_FALSE);
    zpool_close(zhp);
    return rc;
}

// dplane_pool_clear clears error counters on a pool (mirrors `zpool clear`).
static int dplane_pool_clear(libzfs_handle_t *hdl, const char *pool) {
    zpool_handle_t *zhp = zpool_open(hdl, pool);
    if (zhp == NULL) return -1;
    int rc = zpool_clear(zhp, NULL);
    zpool_close(zhp);
    return rc;
}

// dplane_pool_set_property sets a pool-level property.
static int dplane_pool_set_property(libzfs_handle_t *hdl,
                                     const char *pool,
                                     const char *key,
                                     const char *value) {
    zpool_handle_t *zhp = zpool_open(hdl, pool);
    if (zhp == NULL) return -1;
    int rc = zpool_set_prop(zhp, key, value);
    zpool_close(zhp);
    return rc;
}

// dplane_dataset_destroy destroys a ZFS dataset.
// Pass defer_flag=1 to defer destruction (mirrors `zfs destroy -d`).
static int dplane_dataset_destroy(libzfs_handle_t *hdl,
                                   const char *name,
                                   int defer_flag) {
    zfs_handle_t *zhp = zfs_open(hdl, name, ZFS_TYPE_DATASET);
    if (zhp == NULL) return -1;
    int rc = zfs_destroy(zhp, defer_flag ? B_TRUE : B_FALSE);
    zfs_close(zhp);
    return rc;
}

// dplane_snapshot_hold adds a user hold on a snapshot.
static int dplane_snapshot_hold(libzfs_handle_t *hdl,
                                  const char *snapshot,
                                  const char *tag) {
    zfs_handle_t *zhp = zfs_open(hdl, snapshot, ZFS_TYPE_SNAPSHOT);
    if (zhp == NULL) return -1;
    int rc = zfs_hold(zhp, tag, B_FALSE, -1);
    zfs_close(zhp);
    return rc;
}

// dplane_snapshot_release removes a user hold from a snapshot.
static int dplane_snapshot_release(libzfs_handle_t *hdl,
                                     const char *snapshot,
                                     const char *tag) {
    zfs_handle_t *zhp = zfs_open(hdl, snapshot, ZFS_TYPE_SNAPSHOT);
    if (zhp == NULL) return -1;
    int rc = zfs_release(zhp, tag, B_FALSE);
    zfs_close(zhp);
    return rc;
}

// dplane_snapshot_create creates a ZFS snapshot (non-recursive).
static int dplane_snapshot_create(libzfs_handle_t *hdl, const char *name) {
    return zfs_snapshot(hdl, name, B_FALSE, NULL);
}

// dplane_snapshot_destroy destroys a ZFS snapshot.
static int dplane_snapshot_destroy(libzfs_handle_t *hdl, const char *name) {
    zfs_handle_t *zhp = zfs_open(hdl, name, ZFS_TYPE_SNAPSHOT);
    if (zhp == NULL) return -1;
    int rc = zfs_destroy(zhp, B_FALSE);
    zfs_close(zhp);
    return rc;
}

// dplane_snapshot_clone clones a ZFS snapshot into a new dataset.
static int dplane_snapshot_clone(libzfs_handle_t *hdl,
                                  const char *snapshot,
                                  const char *clone) {
    zfs_handle_t *zhp = zfs_open(hdl, snapshot, ZFS_TYPE_SNAPSHOT);
    if (zhp == NULL) return -1;
    int rc = zfs_clone(zhp, clone, NULL);
    zfs_close(zhp);
    return rc;
}

// dplane_last_error returns the current libzfs error description.
static const char *dplane_last_error(libzfs_handle_t *hdl) {
    return libzfs_error_description(hdl);
}
*/
import "C"

import (
	"runtime"
	"unsafe"

	"dplaned/internal/cmdutil"
	"dplaned/internal/security"
)

// withHandle initializes a libzfs handle for the duration of fn, ensuring
// the goroutine is pinned to its OS thread for the entire call (libzfs
// handles are not safe to use across OS threads).
func withHandle(fn func(*C.libzfs_handle_t) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hdl := C.libzfs_init()
	if hdl == nil {
		return libzfsErr("libzfs_init", "failed to initialize handle (is ZFS loaded?)")
	}
	defer C.libzfs_fini(hdl)
	return fn(hdl)
}

// errFromHandle converts a libzfs error into a Go error.
func errFromHandle(hdl *C.libzfs_handle_t, op string) error {
	return libzfsErr(op, C.GoString(C.dplane_last_error(hdl)))
}

// ── Public API ────────────────────────────────────────────────────────────

// PoolIsMember reports whether device is a member vdev of any active pool.
// Uses a direct nvlist walk of the pool config (no subprocess).
func PoolIsMember(device string) (PoolMembership, error) {
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))

	var result PoolMembership
	err := withHandle(func(hdl *C.libzfs_handle_t) error {
		var nameBuf [256]C.char
		found := C.dplane_pool_is_member(hdl, cDevice, &nameBuf[0], 256)
		if found != 0 {
			result.InPool = true
			result.PoolName = C.GoString(&nameBuf[0])
		}
		return nil
	})
	return result, err
}

// PoolImportAll imports all importable pools found under searchPath.
// Errors from individual pool imports are counted; the first failing pool
// does not prevent importing the rest.
func PoolImportAll(searchPath string) error {
	if searchPath == "" {
		searchPath = "/dev/disk/by-id"
	}
	cPath := C.CString(searchPath)
	defer C.free(unsafe.Pointer(cPath))

	return withHandle(func(hdl *C.libzfs_handle_t) error {
		nerr := C.dplane_pool_import_all(hdl, cPath)
		if nerr < 0 {
			return errFromHandle(hdl, "PoolImportAll")
		}
		return nil
	})
}

// PoolExport exports the named pool. Pass force=true to mirror `zpool export -f`.
func PoolExport(name string, force bool) error {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cForce := C.int(0)
	if force {
		cForce = 1
	}
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_pool_export(hdl, cName, cForce); rc != 0 {
			return errFromHandle(hdl, "PoolExport")
		}
		return nil
	})
}

// DatasetCreate creates a ZFS filesystem dataset.
func DatasetCreate(name string) error {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_dataset_create(hdl, cName); rc != 0 {
			return errFromHandle(hdl, "DatasetCreate")
		}
		return nil
	})
}

// DatasetGet returns the value of a ZFS property on a dataset.
func DatasetGet(dataset, prop string) (string, error) {
	cDataset := C.CString(dataset)
	defer C.free(unsafe.Pointer(cDataset))
	cProp := C.CString(prop)
	defer C.free(unsafe.Pointer(cProp))

	var val string
	err := withHandle(func(hdl *C.libzfs_handle_t) error {
		var buf [8192]C.char
		rc := C.dplane_dataset_get_prop(hdl, cDataset, cProp, &buf[0], 8192)
		if rc != 0 {
			return errFromHandle(hdl, "DatasetGet")
		}
		val = C.GoString(&buf[0])
		return nil
	})
	return val, err
}

// DatasetSet sets a ZFS property on a dataset.
func DatasetSet(dataset, prop, value string) error {
	cDataset := C.CString(dataset)
	defer C.free(unsafe.Pointer(cDataset))
	cProp := C.CString(prop)
	defer C.free(unsafe.Pointer(cProp))
	cVal := C.CString(value)
	defer C.free(unsafe.Pointer(cVal))

	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_dataset_set_prop(hdl, cDataset, cProp, cVal); rc != 0 {
			return errFromHandle(hdl, "DatasetSet")
		}
		return nil
	})
}

// DatasetPromote promotes a ZFS clone to a full dataset.
func DatasetPromote(dataset string) error {
	cDataset := C.CString(dataset)
	defer C.free(unsafe.Pointer(cDataset))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_dataset_promote(hdl, cDataset); rc != 0 {
			return errFromHandle(hdl, "DatasetPromote")
		}
		return nil
	})
}

// VdevDetach detaches a device from a ZFS mirror vdev.
func VdevDetach(pool, device string) error {
	cPool := C.CString(pool)
	defer C.free(unsafe.Pointer(cPool))
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))

	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_vdev_detach(hdl, cPool, cDevice); rc != 0 {
			return errFromHandle(hdl, "VdevDetach")
		}
		return nil
	})
}

// VdevOnline brings a pool vdev device back online.
func VdevOnline(pool, device string) error {
	cPool := C.CString(pool)
	defer C.free(unsafe.Pointer(cPool))
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))

	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_vdev_online(hdl, cPool, cDevice); rc != 0 {
			return errFromHandle(hdl, "VdevOnline")
		}
		return nil
	})
}

// VdevOffline takes a pool vdev device offline.
// temporary=true mirrors `zpool offline -t` (device auto-onlines after import).
func VdevOffline(pool, device string, temporary bool) error {
	cPool := C.CString(pool)
	defer C.free(unsafe.Pointer(cPool))
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))
	cTmp := C.int(0)
	if temporary {
		cTmp = 1
	}
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_vdev_offline(hdl, cPool, cDevice, cTmp); rc != 0 {
			return errFromHandle(hdl, "VdevOffline")
		}
		return nil
	})
}

// PoolClear clears error counters on a pool device.
func PoolClear(pool string) error {
	cPool := C.CString(pool)
	defer C.free(unsafe.Pointer(cPool))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_pool_clear(hdl, cPool); rc != 0 {
			return errFromHandle(hdl, "PoolClear")
		}
		return nil
	})
}

// PoolSetProperty sets a pool-level property.
func PoolSetProperty(pool, key, value string) error {
	cPool := C.CString(pool)
	defer C.free(unsafe.Pointer(cPool))
	cKey := C.CString(key)
	defer C.free(unsafe.Pointer(cKey))
	cVal := C.CString(value)
	defer C.free(unsafe.Pointer(cVal))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_pool_set_property(hdl, cPool, cKey, cVal); rc != 0 {
			return errFromHandle(hdl, "PoolSetProperty")
		}
		return nil
	})
}

// DatasetDestroy destroys a ZFS dataset. Pass recursive=true to recursively
// destroy all child datasets and snapshots first (use with caution).
func DatasetDestroy(name string, recursive bool) error {
	if err := security.ValidateDatasetName(name); err != nil {
		return libzfsErr("DatasetDestroy", err.Error())
	}
	if recursive {
		// Recursive destroy: fall back to subprocess which handles child iteration.
		args := []string{"destroy", "-r", name}
		out, err := cmdutil.RunMedium("zfs_destroy", args...)
		if err != nil {
			return libzfsErr("DatasetDestroy", string(out))
		}
		return nil
	}
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_dataset_destroy(hdl, cName, 0); rc != 0 {
			return errFromHandle(hdl, "DatasetDestroy")
		}
		return nil
	})
}

// SnapshotHold adds a user hold on a snapshot to prevent it from being deleted.
func SnapshotHold(tag, snapshot string) error {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return libzfsErr("SnapshotHold", err.Error())
	}
	cSnap := C.CString(snapshot)
	defer C.free(unsafe.Pointer(cSnap))
	cTag := C.CString(tag)
	defer C.free(unsafe.Pointer(cTag))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_snapshot_hold(hdl, cSnap, cTag); rc != 0 {
			return errFromHandle(hdl, "SnapshotHold")
		}
		return nil
	})
}

// SnapshotRelease removes a user hold from a snapshot.
func SnapshotRelease(tag, snapshot string) error {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return libzfsErr("SnapshotRelease", err.Error())
	}
	cSnap := C.CString(snapshot)
	defer C.free(unsafe.Pointer(cSnap))
	cTag := C.CString(tag)
	defer C.free(unsafe.Pointer(cTag))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_snapshot_release(hdl, cSnap, cTag); rc != 0 {
			return errFromHandle(hdl, "SnapshotRelease")
		}
		return nil
	})
}

// SnapshotCreate creates a ZFS snapshot.
func SnapshotCreate(name string) error {
	if err := security.ValidateSnapshotName(name); err != nil {
		return libzfsErr("SnapshotCreate", err.Error())
	}
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_snapshot_create(hdl, cName); rc != 0 {
			return errFromHandle(hdl, "SnapshotCreate")
		}
		return nil
	})
}

// SnapshotDestroy destroys a ZFS snapshot.
func SnapshotDestroy(name string) error {
	if err := security.ValidateSnapshotName(name); err != nil {
		return libzfsErr("SnapshotDestroy", err.Error())
	}
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_snapshot_destroy(hdl, cName); rc != 0 {
			return errFromHandle(hdl, "SnapshotDestroy")
		}
		return nil
	})
}

// SnapshotClone clones a ZFS snapshot into a new dataset.
func SnapshotClone(snapshot, clone string) error {
	if err := security.ValidateSnapshotName(snapshot); err != nil {
		return libzfsErr("SnapshotClone", err.Error())
	}
	if err := security.ValidateDatasetName(clone); err != nil {
		return libzfsErr("SnapshotClone", err.Error())
	}
	cSnapshot := C.CString(snapshot)
	defer C.free(unsafe.Pointer(cSnapshot))
	cClone := C.CString(clone)
	defer C.free(unsafe.Pointer(cClone))
	return withHandle(func(hdl *C.libzfs_handle_t) error {
		if rc := C.dplane_snapshot_clone(hdl, cSnapshot, cClone); rc != 0 {
			return errFromHandle(hdl, "SnapshotClone")
		}
		return nil
	})
}
