export type JavaLspSettings = {
  java: {
    import: {
      maven: { enabled: boolean }
      gradle: { enabled: boolean }
      generatesMetadataFilesAtProjectRoot: boolean
    }
    autobuild: { enabled: boolean }
    configuration: { updateBuildConfiguration: 'automatic' | 'disabled' }
  }
}

/** Keep project metadata and automatic builds out of read-only checkouts. */
export function javaLspSettings(readOnly: boolean): JavaLspSettings {
  return {
    java: {
      import: {
        maven: { enabled: true },
        gradle: { enabled: true },
        generatesMetadataFilesAtProjectRoot: !readOnly,
      },
      autobuild: { enabled: !readOnly },
      configuration: { updateBuildConfiguration: readOnly ? 'disabled' : 'automatic' },
    },
  }
}
