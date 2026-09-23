import { Button, Card, Popconfirm, Select, Table, message } from 'antd'
import { useEffect, useState } from 'react'
import { listMyApplications, listOrgApplications, updateApplicationStatus, withdrawApplication } from '@/api/application'
import ApplicationStatusBadge from '@/components/common/ApplicationStatusBadge'
import {
  ApplicationStatusMap,
  isWithdrawableStatus,
  NON_PROGRESSABLE_STATUSES,
  type ApplicationStatus,
} from '@/constants/application'
import { useAuth } from '@/hooks/useAuth'
import type { AdoptionApplication } from '@/types/api'
import { formatDate } from '@/utils/dateFormat'

export default function Applications() {
  const { isOrg } = useAuth()
  const [apps, setApps] = useState<AdoptionApplication[]>([])
  const [status, setStatus] = useState('')

  async function load(s = status) {
    if (isOrg) {
      setApps(await listOrgApplications(s))
    } else {
      setApps(await listMyApplications())
    }
  }
  useEffect(() => {
    load()
  }, [isOrg])

  async function changeStatus(id: number, next: string) {
    try {
      await updateApplicationStatus(id, next)
      message.success('状态已更新')
    } finally {
      // Refresh either way: on a 409 the list must converge with a concurrent
      // withdrawal/org decision.
      await load()
    }
  }

  async function withdraw(id: number) {
    try {
      await withdrawApplication(id)
      message.success('申请已撤回')
    } catch {
      // The request interceptor surfaces the error; still refresh so the list
      // reflects the org's freshly pushed status.
    } finally {
      await load()
    }
  }

  const statusOptions = (Object.keys(ApplicationStatusMap) as ApplicationStatus[]).map((s) => ({
    value: s,
    label: ApplicationStatusMap[s].text,
  }))

  return (
    <div>
      <h1>领养申请</h1>
      {isOrg && (
        <Select
          style={{ width: 180, marginBottom: 12 }}
          placeholder="按状态筛选"
          allowClear
          value={status || undefined}
          onChange={(v) => {
            setStatus(v || '')
            load(v || '')
          }}
          options={statusOptions}
        />
      )}
      <Table
        rowKey="id"
        dataSource={apps}
        pagination={false}
        columns={[
          { title: 'ID', dataIndex: 'id' },
          { title: '宠物 ID', dataIndex: 'pet_id' },
          { title: '申请时间', dataIndex: 'created_at', render: (v: string) => formatDate(v) },
          {
            title: '撤回时间',
            dataIndex: 'withdrawn_at',
            render: (v: string | null) => (v ? formatDate(v) : '—'),
          },
          { title: '状态', dataIndex: 'status', render: (v: string) => <ApplicationStatusBadge status={v} /> },
          {
            title: '操作',
            render: (_, r) =>
              isOrg ? (
                NON_PROGRESSABLE_STATUSES.includes(r.status as ApplicationStatus) ? null : (
                  <Button size="small" onClick={() => changeStatus(r.id, r.status === 'submitted' ? 'org_review' : 'approved')}>
                    推进审核
                  </Button>
                )
              ) : isWithdrawableStatus(r.status) ? (
                <Popconfirm
                  title="确认撤回该领养申请？"
                  description="撤回后宠物将重新开放领养。"
                  okText="撤回"
                  cancelText="取消"
                  onConfirm={() => withdraw(r.id)}
                >
                  <Button size="small" danger>
                    撤回申请
                  </Button>
                </Popconfirm>
              ) : null,
          },
        ]}
      />
      {!apps.length && <Card style={{ marginTop: 12 }}>暂无申请记录</Card>}
    </div>
  )
}
