import { Button, Card, Popconfirm, Select, Table, message } from 'antd'
import { useEffect, useState } from 'react'
import {
  listMyApplications,
  listOrgApplications,
  updateApplicationStatus,
  withdrawApplication,
} from '@/api/application'
import ApplicationStatusBadge from '@/components/common/ApplicationStatusBadge'
import { ApplicationStatusMap, isWithdrawable } from '@/constants/application'
import { useAuth } from '@/hooks/useAuth'
import type { AdoptionApplication } from '@/types/api'
import { formatDate, formatDateTime } from '@/utils/dateFormat'

// Terminal statuses the org can no longer advance.
const TERMINAL_STATUSES = ['approved', 'rejected', 'withdrawn']

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOrg])

  async function changeStatus(id: number, next: string) {
    try {
      await updateApplicationStatus(id, next)
      message.success('状态已更新')
    } finally {
      // Always refresh so the view converges with concurrent decisions.
      await load()
    }
  }

  async function withdraw(id: number) {
    try {
      await withdrawApplication(id)
      message.success('申请已撤回')
    } catch {
      // The interceptor already surfaced the error; refresh so the org's
      // just-committed status (e.g. offline_interview) is shown.
    } finally {
      await load()
    }
  }

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
          options={Object.keys(ApplicationStatusMap).map((s) => ({ value: s, label: s }))}
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
            render: (v: string | null) => formatDateTime(v || undefined),
          },
          { title: '状态', dataIndex: 'status', render: (v: string) => <ApplicationStatusBadge status={v} /> },
          {
            title: '操作',
            render: (_, r) =>
              isOrg ? (
                TERMINAL_STATUSES.includes(r.status) ? null : (
                  <Button size="small" onClick={() => changeStatus(r.id, r.status === 'submitted' ? 'org_review' : 'approved')}>
                    推进审核
                  </Button>
                )
              ) : isWithdrawable(r.status) ? (
                <Popconfirm
                  title="确认撤回该领养申请？"
                  description="撤回后该宠物将重新开放领养。"
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
