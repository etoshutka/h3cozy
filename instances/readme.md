query {
  getInstanceList(project_id: "myproj") {
    instance_id
    project_id
    name
    status
    created
    updated
    key_name
    locked
    power_state
    ipV4
    attachedDisks {
      disk_id
    }
  }
}