#ifndef WAILS_SSH_NATIVE_PICKER_H
#define WAILS_SSH_NATIVE_PICKER_H

typedef struct {
    char *paths_json;
    char *error_message;
} SSHUploadPickerResult;

// Both strings are allocated in C and must be freed by the caller.
SSHUploadPickerResult ssh_pick_upload_paths(void);

#endif
